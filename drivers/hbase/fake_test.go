package hbase

import (
	"io"
	"sort"

	"github.com/tsuna/gohbase/hrpc"
	"github.com/tsuna/gohbase/pb"
)

// fakeClient is an in-memory hbaseClient: it models one table as
// rowkey → family → qualifier → value, so the Store's read, write, and scan paths
// run deterministically without a region server. It ignores server-side filters (a
// scan returns every row), which is correct for the Store's plumbing — filter
// translation is unit-tested separately in filter_test.go, and the query engine
// re-runs the full jq over whatever a scan returns.
type fakeClient struct {
	rows       map[string]map[string]map[string][]byte
	scanErr    error // when set, a scan's first Next returns this
	getErr     error // when set, Get returns this
	putCalls   int
	delCalls   int
	casCalls   int
	closeCalls int
}

func newFakeClient() *fakeClient {
	return &fakeClient{rows: map[string]map[string]map[string][]byte{}}
}

// seed inserts a row's cells directly, bypassing the write path.
func (f *fakeClient) seed(key string, cells map[string]map[string][]byte) {
	f.rows[key] = cells
}

func (f *fakeClient) Get(g *hrpc.Get) (*hrpc.Result, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &hrpc.Result{Cells: cellsFor(string(g.Key()), f.rows[string(g.Key())])}, nil
}

func (f *fakeClient) Put(p *hrpc.Mutate) (*hrpc.Result, error) {
	f.putCalls++
	f.apply(string(p.Key()), p.Values())
	return &hrpc.Result{}, nil
}

func (f *fakeClient) Delete(d *hrpc.Mutate) (*hrpc.Result, error) {
	f.delCalls++
	values := d.Values()
	if len(values) == 0 {
		delete(f.rows, string(d.Key()))
		return &hrpc.Result{}, nil
	}
	row := f.rows[string(d.Key())]
	for family, quals := range values {
		for q := range quals {
			delete(row[family], q)
		}
	}
	return &hrpc.Result{}, nil
}

func (f *fakeClient) CheckAndPut(p *hrpc.Mutate, family, qualifier string, expectedValue []byte) (bool, error) {
	f.casCalls++
	// Only the put-if-absent form (expectedValue nil) is exercised by the Store.
	if expectedValue == nil {
		if row, ok := f.rows[string(p.Key())]; ok {
			if _, exists := row[family][qualifier]; exists {
				return false, nil
			}
		}
	}
	f.apply(string(p.Key()), p.Values())
	return true, nil
}

// apply merges a put's cells into the store, creating families as needed.
func (f *fakeClient) apply(key string, values map[string]map[string][]byte) {
	row := f.rows[key]
	if row == nil {
		row = map[string]map[string][]byte{}
		f.rows[key] = row
	}
	for family, quals := range values {
		if row[family] == nil {
			row[family] = map[string][]byte{}
		}
		for q, v := range quals {
			row[family][q] = v
		}
	}
}

func (f *fakeClient) Scan(s *hrpc.Scan) hrpc.Scanner {
	keys := make([]string, 0, len(f.rows))
	for k := range f.rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	results := make([]*hrpc.Result, 0, len(keys))
	for _, k := range keys {
		results = append(results, &hrpc.Result{Cells: cellsFor(k, f.rows[k])})
	}
	return &fakeScanner{results: results, err: f.scanErr}
}

func (f *fakeClient) Close() { f.closeCalls++ }

// cellsFor builds the sorted cell slice a Get/Scan result carries for one row, so
// results are deterministic.
func cellsFor(key string, row map[string]map[string][]byte) []*hrpc.Cell {
	if row == nil {
		return nil
	}
	families := make([]string, 0, len(row))
	for fam := range row {
		families = append(families, fam)
	}
	sort.Strings(families)
	var cells []*hrpc.Cell
	for _, fam := range families {
		quals := make([]string, 0, len(row[fam]))
		for q := range row[fam] {
			quals = append(quals, q)
		}
		sort.Strings(quals)
		for _, q := range quals {
			cells = append(cells, &hrpc.Cell{
				Row:       []byte(key),
				Family:    []byte(fam),
				Qualifier: []byte(q),
				Value:     row[fam][q],
			})
		}
	}
	return cells
}

// fakeScanner replays a fixed slice of results, returning io.EOF when drained, the
// contract the Store's scan loops depend on.
type fakeScanner struct {
	results []*hrpc.Result
	i       int
	err     error
	closed  bool
}

func (s *fakeScanner) Next() (*hrpc.Result, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.i >= len(s.results) {
		return nil, io.EOF
	}
	r := s.results[s.i]
	s.i++
	return r, nil
}

func (s *fakeScanner) Close() error { s.closed = true; return nil }

func (s *fakeScanner) GetScanMetrics() map[string]int64 { return nil }

// fakeAdmin is an in-memory hbaseAdmin for the list, disable, and drop paths.
type fakeAdmin struct {
	tables     []*pb.TableName
	clusterErr error
	disabled   []string
	deleted    []string
	listErr    error
}

func (a *fakeAdmin) ClusterStatus() (*pb.ClusterStatus, error) {
	return &pb.ClusterStatus{}, a.clusterErr
}

func (a *fakeAdmin) ListTableNames(t *hrpc.ListTableNames) ([]*pb.TableName, error) {
	return a.tables, a.listErr
}

func (a *fakeAdmin) DisableTable(t *hrpc.DisableTable) error {
	a.disabled = append(a.disabled, string(t.Table()))
	return nil
}

func (a *fakeAdmin) DeleteTable(t *hrpc.DeleteTable) error {
	a.deleted = append(a.deleted, string(t.Table()))
	return nil
}

// tableName builds a pb.TableName for a fakeAdmin's table list.
func tableName(namespace, qualifier string) *pb.TableName {
	return &pb.TableName{Namespace: []byte(namespace), Qualifier: []byte(qualifier)}
}

// newFakeStore builds a table-scoped Store backed by the fakes, with a small page
// size so paging is exercised by a handful of rows.
func newFakeStore(client hbaseClient, admin hbaseAdmin, table string, types typeMap, rowkey colType) *Store {
	return &Store{
		client:     client,
		admin:      admin,
		table:      table,
		types:      types,
		rowkeyType: rowkey,
		pageSize:   2,
	}
}

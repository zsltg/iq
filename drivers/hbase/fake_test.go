package hbase

import (
	"context"
	"io"
	"maps"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tsuna/gohbase/hrpc"
	"github.com/tsuna/gohbase/pb"
	"github.com/tsuna/gohbase/region"
)

// ctxKey is the type of the marker a test context carries. A request assertion reads
// it back to prove the call bound the caller's own context rather than a substituted
// or empty one.
type ctxKey struct{}

// ctxMarker is the value every test context stores under ctxKey.
const ctxMarker = "iq-hbase-test-context"

// testCtx returns the context integration-free tests pass in, carrying the marker
// requireTestCtx looks for.
func testCtx() context.Context {
	return context.WithValue(context.Background(), ctxKey{}, ctxMarker)
}

// requireTestCtx requires that a recorded request carried the context testCtx built.
// It is what pins the ctx argument of every hrpc constructor: a nil or replaced
// context has no marker.
func requireTestCtx(t *testing.T, ctx context.Context) {
	t.Helper()
	require.NotNil(t, ctx, "the request carried no context")
	require.Equal(t, ctxMarker, ctx.Value(ctxKey{}))
}

// rpcCall is one request the fake received, kept so a test can assert what the Store
// asked the region server for — the context it bound, the table it named, and the
// protobuf the server would see — not only what came back.
type rpcCall struct {
	ctx   context.Context
	table string
	req   any
}

// recordCall snapshots a request. ToProto needs a routed region, which only the real
// client assigns, so a stub one is attached first; the fake dispatches nothing, so
// the stub is used for nothing else.
func recordCall(c hrpc.Call) rpcCall {
	c.SetRegion(region.NewInfo(0, nil, c.Table(), []byte("region"), nil, nil))
	return rpcCall{ctx: c.Context(), table: string(c.Table()), req: c.ToProto()}
}

// scanPB returns a recorded scan's inner pb.Scan, the wire form carrying its filter
// and version limit.
func scanPB(t *testing.T, rec rpcCall) *pb.Scan {
	t.Helper()
	req, ok := rec.req.(*pb.ScanRequest)
	require.True(t, ok, "recorded request is not a scan")
	require.NotNil(t, req.Scan)
	return req.Scan
}

// getPB returns a recorded get's inner pb.Get, the wire form carrying its version
// limit and existence-only flag.
func getPB(t *testing.T, rec rpcCall) *pb.Get {
	t.Helper()
	req, ok := rec.req.(*pb.GetRequest)
	require.True(t, ok, "recorded request is not a get")
	require.NotNil(t, req.Get)
	return req.Get
}

// requireDefaultMaxVersions requires a request to ask for exactly one version. The
// gohbase default is one, so the option leaves the proto field unset; any other count
// sets it, which is what distinguishes MaxVersions(1) from its neighbours.
func requireDefaultMaxVersions(t *testing.T, versions *uint32) {
	t.Helper()
	require.Nil(t, versions, "the request asked for a version count other than one")
}

// requireFilter requires a request's filter to be exactly the wire form of want, name
// and serialized body alike, so a differently-configured filter of the same kind fails.
func requireFilter(t *testing.T, got *pb.Filter, want filterConstructor) {
	t.Helper()
	require.NotNil(t, got, "the request carried no server-side filter")
	expected, err := want.ConstructPBFilter()
	require.NoError(t, err)
	require.Equal(t, expected.GetName(), got.GetName())
	require.Equal(t, expected.GetSerializedFilter(), got.GetSerializedFilter())
}

// filterConstructor is the one method requireFilter needs from a gohbase filter.
type filterConstructor interface {
	ConstructPBFilter() (*pb.Filter, error)
}

// fakeClient is an in-memory hbaseClient: it models one table as
// rowkey → family → qualifier → value, so the Store's read, write, and scan paths
// run deterministically without a region server. It applies no server-side filter (a
// scan returns every row), which is correct for the Store's plumbing — filter
// translation is unit-tested separately in filter_test.go, the request it issues is
// asserted from the recorded calls, and the query engine re-runs the full jq over
// whatever a scan returns.
type fakeClient struct {
	rows       map[string]map[string]map[string][]byte
	scanErr    error // when set, a scan's first Next returns this
	getErr     error // when set, Get returns this
	putErr     error // when set, Put returns this
	delErr     error // when set, Delete returns this
	putCalls   int
	delCalls   int
	casCalls   int
	closeCalls int
	gets       []rpcCall
	puts       []rpcCall
	dels       []rpcCall
	scans      []rpcCall
	cas        []rpcCall
}

func newFakeClient() *fakeClient {
	return &fakeClient{rows: map[string]map[string]map[string][]byte{}}
}

// seed inserts a row's cells directly, bypassing the write path.
func (f *fakeClient) seed(key string, cells map[string]map[string][]byte) {
	f.rows[key] = cells
}

func (f *fakeClient) Get(g *hrpc.Get) (*hrpc.Result, error) {
	f.gets = append(f.gets, recordCall(g))
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &hrpc.Result{Cells: cellsFor(string(g.Key()), f.rows[string(g.Key())])}, nil
}

func (f *fakeClient) Put(p *hrpc.Mutate) (*hrpc.Result, error) {
	f.putCalls++
	f.puts = append(f.puts, recordCall(p))
	if f.putErr != nil {
		return nil, f.putErr
	}
	f.apply(string(p.Key()), p.Values())
	return &hrpc.Result{}, nil
}

func (f *fakeClient) Delete(d *hrpc.Mutate) (*hrpc.Result, error) {
	f.delCalls++
	values := d.Values()
	f.dels = append(f.dels, recordCall(d))
	if f.delErr != nil {
		return nil, f.delErr
	}
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
	f.cas = append(f.cas, recordCall(p))
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
		maps.Copy(row[family], quals)
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
	f.scans = append(f.scans, recordCall(s))
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
	tables       []*pb.TableName
	clusterErr   error
	disabled     []string
	deleted      []string
	listErr      error
	disableErr   error
	deleteErr    error
	listCalls    []rpcCall
	disableCalls []rpcCall
	deleteCalls  []rpcCall
}

func (a *fakeAdmin) ClusterStatus() (*pb.ClusterStatus, error) {
	return &pb.ClusterStatus{}, a.clusterErr
}

func (a *fakeAdmin) ListTableNames(t *hrpc.ListTableNames) ([]*pb.TableName, error) {
	a.listCalls = append(a.listCalls, recordCall(t))
	return a.tables, a.listErr
}

func (a *fakeAdmin) DisableTable(t *hrpc.DisableTable) error {
	a.disableCalls = append(a.disableCalls, recordCall(t))
	if a.disableErr != nil {
		return a.disableErr
	}
	a.disabled = append(a.disabled, string(t.Table()))
	return nil
}

func (a *fakeAdmin) DeleteTable(t *hrpc.DeleteTable) error {
	a.deleteCalls = append(a.deleteCalls, recordCall(t))
	if a.deleteErr != nil {
		return a.deleteErr
	}
	a.deleted = append(a.deleted, string(t.Table()))
	return nil
}

// listedNamespace returns the namespace a recorded ListTableNames asked for.
func listedNamespace(t *testing.T, rec rpcCall) string {
	t.Helper()
	req, ok := rec.req.(*pb.GetTableNamesRequest)
	require.True(t, ok, "recorded request is not a table listing")
	return req.GetNamespace()
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

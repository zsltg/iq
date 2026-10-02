package hbase

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

var errStop = errors.New("stop the scan")

// scanCase is one scan or clear run over a fake table: how the fake fails, what the
// call returns, and how many times the scanner must be closed.
type scanCase struct {
	name       string
	rows       []string
	scanErr    error
	failAt     int
	delErr     error
	run        func(st *Store) error
	wantErr    error
	wantText   string
	wantCloses int
}

func scanBatchesWith(fn func(map[string]any) error) func(*Store) error {
	return func(st *Store) error { return st.ScanBatches(context.Background(), fn) }
}

func scanFilteredWith(fn func(map[string]any) error) func(*Store) error {
	return func(st *Store) error { return st.ScanFiltered(context.Background(), nil, fn) }
}

func execWith(args ...string) func(*Store) error {
	return func(st *Store) error {
		_, err := st.Query(context.Background(), args)
		return err
	}
}

func clearTable(st *Store) error { return st.Clear(context.Background()) }

func keepAll(map[string]any) error { return nil }

func failAlways(map[string]any) error { return errStop }

// TestScannerCloseRules pins when each scan loop closes its scanner. A scanner closes
// once when Next fails, once when the callback fails inside a full page, and once when
// a limit stops the scan. It never closes after io.EOF, and that includes a callback
// failure on the final partial page, which runs after the scanner returned io.EOF.
func TestScannerCloseRules(t *testing.T) {
	tests := []scanCase{
		{
			name: "ScanBatches closes on a first Next error", rows: []string{"1", "2"}, scanErr: errBackend,
			run: scanBatchesWith(keepAll), wantErr: errBackend, wantText: "hbase scan", wantCloses: 1,
		},
		{
			name: "ScanBatches closes on a Next error after rows", rows: []string{"1", "2", "3"}, scanErr: errBackend, failAt: 1,
			run: scanBatchesWith(keepAll), wantErr: errBackend, wantText: "hbase scan", wantCloses: 1,
		},
		{
			name: "ScanBatches closes on a callback error in a full page", rows: []string{"1", "2", "3", "4"},
			run: scanBatchesWith(failAlways), wantErr: errStop, wantCloses: 1,
		},
		{
			name: "ScanBatches does not close on a callback error in the final partial page", rows: []string{"1"},
			run: scanBatchesWith(failAlways), wantErr: errStop, wantCloses: 0,
		},
		{
			name: "ScanBatches does not close on a full scan", rows: []string{"1", "2", "3"},
			run: scanBatchesWith(keepAll), wantCloses: 0,
		},
		{
			name: "ScanFiltered closes on a Next error", rows: []string{"1"}, scanErr: errBackend,
			run: scanFilteredWith(keepAll), wantErr: errBackend, wantText: "hbase scan", wantCloses: 1,
		},
		{
			name: "ScanFiltered closes on a callback error in a full page", rows: []string{"1", "2"},
			run: scanFilteredWith(failAlways), wantErr: errStop, wantCloses: 1,
		},
		{
			name: "ScanFiltered does not close on a callback error in the final partial page", rows: []string{"1", "2", "3"},
			run: func(st *Store) error {
				calls := 0
				return st.ScanFiltered(context.Background(), nil, func(map[string]any) error {
					calls++
					if calls == 2 {
						return errStop
					}
					return nil
				})
			}, wantErr: errStop, wantCloses: 0,
		},
		{
			name: "exec scan closes on a Next error", rows: []string{"1", "2"}, scanErr: errBackend, failAt: 1,
			run: execWith("scan", "books"), wantErr: errBackend, wantText: "hbase scan", wantCloses: 1,
		},
		{
			name: "exec scan closes when the limit stops it", rows: []string{"1", "2", "3"},
			run: execWith("scan", "books", "2"), wantCloses: 1,
		},
		{
			name: "exec scan closes when the limit meets the last row", rows: []string{"1", "2"},
			run: execWith("scan", "books", "2"), wantCloses: 1,
		},
		{
			name: "exec scan does not close below the limit", rows: []string{"1", "2"},
			run: execWith("scan", "books", "3"), wantCloses: 0,
		},
		{
			name: "exec scan does not close with no limit", rows: []string{"1", "2", "3"},
			run: execWith("scan", "books"), wantCloses: 0,
		},
		{
			name: "exec scan does not close with a zero limit", rows: []string{"1", "2", "3"},
			run: execWith("scan", "books", "0"), wantCloses: 0,
		},
		{
			name: "exec scan does not close with a negative limit", rows: []string{"1", "2", "3"},
			run: execWith("scan", "books", "-1"), wantCloses: 0,
		},
		{
			name: "exec count closes on a Next error", rows: []string{"1", "2"}, scanErr: errBackend, failAt: 1,
			run: execWith("count", "books"), wantErr: errBackend, wantText: "hbase count", wantCloses: 1,
		},
		{
			name: "exec count does not close on a full scan", rows: []string{"1", "2", "3"},
			run: execWith("count", "books"), wantCloses: 0,
		},
		{
			name: "Clear closes on a Next error", rows: []string{"1", "2"}, scanErr: errBackend, failAt: 1,
			run: clearTable, wantErr: errBackend, wantText: "hbase clear scan", wantCloses: 1,
		},
		{
			name: "Clear closes on a delete error", rows: []string{"1", "2"}, delErr: errBackend,
			run: clearTable, wantErr: errBackend, wantText: "hbase clear delete", wantCloses: 1,
		},
		{
			name: "Clear does not close on a full scan", rows: []string{"1", "2", "3"},
			run: clearTable, wantCloses: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClient()
			for _, k := range tt.rows {
				fc.seed(k, cell("cf", "n", k))
			}
			fc.scanErr, fc.scanFailAt, fc.delErr = tt.scanErr, tt.failAt, tt.delErr
			st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto) // pageSize 2

			err := tt.run(st)

			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}
			if tt.wantText != "" {
				require.ErrorContains(t, err, tt.wantText)
			}
			require.Len(t, fc.scanners, 1)
			require.Equal(t, tt.wantCloses, fc.scanners[0].closes)
		})
	}
}

// TestCallbackErrorReturnsUnwrapped pins that a callback error leaves the scan as the
// very value the callback returned, with no wrap added.
func TestCallbackErrorReturnsUnwrapped(t *testing.T) {
	tests := []struct {
		name string
		rows []string
	}{
		{"a full page", []string{"1", "2"}},
		{"the final partial page", []string{"1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClient()
			for _, k := range tt.rows {
				fc.seed(k, cell("cf", "n", k))
			}
			st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

			err := st.ScanBatches(context.Background(), failAlways)

			require.Equal(t, errStop, err)
		})
	}
}

// TestEmptyRowsAreSkipped pins that a row with no cells between two rows is skipped by
// every scan loop: it is not counted, paged, returned, or deleted.
func TestEmptyRowsAreSkipped(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, fc *fakeClient, st *Store)
	}{
		{"exec count counts the rows with cells", func(t *testing.T, _ *fakeClient, st *Store) {
			got, err := st.Query(context.Background(), []string{"count", "books"})
			require.NoError(t, err)
			require.Equal(t, 2, got)
		}},
		{"ScanBatches pages the rows with cells", func(t *testing.T, _ *fakeClient, st *Store) {
			var pages [][]string
			err := st.ScanBatches(context.Background(), func(b map[string]any) error {
				var keys []string
				for k := range b {
					keys = append(keys, k)
				}
				pages = append(pages, keys)
				return nil
			})
			require.NoError(t, err)
			require.Len(t, pages, 1)
			require.ElementsMatch(t, []string{"1", "3"}, pages[0])
		}},
		{"exec scan returns the rows with cells and counts them toward the limit", func(t *testing.T, _ *fakeClient, st *Store) {
			got, err := st.Query(context.Background(), []string{"scan", "books", "2"})
			require.NoError(t, err)
			rows, ok := got.(map[string]any)
			require.True(t, ok)
			require.Len(t, rows, 2)
			require.Contains(t, rows, "1")
			require.Contains(t, rows, "3")
		}},
		{"Clear deletes the rows with cells", func(t *testing.T, fc *fakeClient, st *Store) {
			require.NoError(t, st.Clear(context.Background()))
			require.Len(t, fc.dels, 2)
			require.Equal(t, 2, fc.delCalls)
			require.Contains(t, fc.rows, "2")
			require.NotContains(t, fc.rows, "1")
			require.NotContains(t, fc.rows, "3")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClient()
			fc.seed("1", cell("cf", "n", "1"))
			fc.seed("2", map[string]map[string][]byte{})
			fc.seed("3", cell("cf", "n", "3"))
			st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

			tt.run(t, fc, st)
		})
	}
}

// TestPutValueErrorWinsOverKeyError pins that Put checks the value before the row key,
// so a record with both faults reports the value, and it writes nothing.
func TestPutValueErrorWinsOverKeyError(t *testing.T) {
	tests := []struct {
		name string
		mode query.WriteMode
	}{
		{"upsert", query.Upsert},
		{"insert only", query.InsertOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClient()
			st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctLong)
			batch := []query.Record{{Key: "not-a-number", Value: "scalar"}}

			stat, err := st.Put(context.Background(), batch, tt.mode)

			require.ErrorContains(t, err, "record value must be")
			require.Equal(t, query.WriteStat{}, stat)
			require.Zero(t, fc.putCalls+fc.casCalls)
			require.Empty(t, fc.gets)
		})
	}
}

// TestPutErrorDropsThePartialStat pins that Put reports a zero WriteStat on an error
// after earlier records were written.
func TestPutErrorDropsThePartialStat(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	good := map[string]any{"cf": map[string]any{"n": "1"}}
	batch := []query.Record{{Key: "1", Value: good}, {Key: "2", Value: "scalar"}}

	stat, err := st.Put(context.Background(), batch, query.Upsert)

	require.Error(t, err)
	require.Equal(t, query.WriteStat{}, stat)
	require.Equal(t, 1, fc.putCalls)
}

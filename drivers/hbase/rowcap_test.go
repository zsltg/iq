package hbase

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// wideRow returns one family with n cells.
func wideRow(family string, n int) map[string]map[string][]byte {
	quals := make(map[string][]byte, n)
	for i := range n {
		quals[fmt.Sprintf("q%03d", i)] = []byte("v")
	}
	return map[string]map[string][]byte{family: quals}
}

// rowReaders lists the read paths that return a whole row.
var rowReaders = []struct {
	name string
	read func(ctx context.Context, st *Store) error
}{
	{"Get", func(ctx context.Context, st *Store) error {
		_, err := st.Get(ctx, []string{"k1"})
		return err
	}},
	{"ScanBatches", func(ctx context.Context, st *Store) error {
		return st.ScanBatches(ctx, func(map[string]any) error { return nil })
	}},
	{"execGet", func(ctx context.Context, st *Store) error {
		_, err := st.Query(ctx, []string{"get", "books", "k1"})
		return err
	}},
	{"execScan", func(ctx context.Context, st *Store) error {
		_, err := st.Query(ctx, []string{"scan", "books"})
		return err
	}},
}

func cappedStore(rowCells int) (*Store, *fakeClient) {
	fc := newFakeClient()
	fc.seed("k1", wideRow("cf", rowCells))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	st.maxRowCells = 6
	return st, fc
}

// TestRowCapRequest pins that a bounded read asks the server for at most limit+1
// cells per column family, and an unbounded read sets no cap.
func TestRowCapRequest(t *testing.T) {
	for _, rd := range rowReaders {
		t.Run(rd.name+" bounded", func(t *testing.T) {
			st, fc := cappedStore(2)

			require.NoError(t, rd.read(context.Background(), st))
			require.Equal(t, uint32(7), storeLimitOf(t, fc))
		})
		t.Run(rd.name+" unbounded", func(t *testing.T) {
			st, fc := cappedStore(2)

			require.NoError(t, rd.read(query.WithUnbounded(context.Background()), st))
			require.Zero(t, storeLimitOf(t, fc))
		})
	}
}

// TestRowCapRequestFiltered pins the same cap on a filtered scan.
func TestRowCapRequestFiltered(t *testing.T) {
	st, fc := cappedStore(2)

	require.NoError(t, st.ScanFiltered(context.Background(), nil, func(map[string]any) error { return nil }))
	require.Equal(t, uint32(7), scanPB(t, fc.scans[0]).GetStoreLimit())
}

// storeLimitOf returns the per-family cell limit of the one recorded request, or 0
// when the request carries none.
func storeLimitOf(t *testing.T, fc *fakeClient) uint32 {
	t.Helper()
	if len(fc.gets) == 1 {
		return getPB(t, fc.gets[0]).GetStoreLimit()
	}
	require.Len(t, fc.scans, 1)
	return scanPB(t, fc.scans[0]).GetStoreLimit()
}

// TestRowCapRefusal pins which rows a bounded read refuses, and the error text.
func TestRowCapRefusal(t *testing.T) {
	const want = `read row "k1": too many cells: a column family has more than 6 cells, so read it with --unbounded`
	tests := []struct {
		name      string
		cells     int
		unbounded bool
		refused   bool
	}{
		{"at the limit", 6, false, false},
		{"one over", 7, false, true},
		{"far over", 9, false, true},
		{"unbounded reads the large row", 50, true, false},
	}
	for _, rd := range rowReaders {
		for _, tt := range tests {
			t.Run(rd.name+" "+tt.name, func(t *testing.T) {
				st, _ := cappedStore(tt.cells)
				ctx := context.Background()
				if tt.unbounded {
					ctx = query.WithUnbounded(ctx)
				}

				err := rd.read(ctx, st)

				if !tt.refused {
					require.NoError(t, err)
					return
				}
				require.ErrorIs(t, err, ErrRowTooLarge)
				require.EqualError(t, err, want)
			})
		}
	}
}

// TestRowCapCountsEachFamily pins that the limit applies to one column family, as
// the server applies it, not to the row total.
func TestRowCapCountsEachFamily(t *testing.T) {
	st, fc := cappedStore(0)
	row := wideRow("a", 4)
	row["b"] = wideRow("b", 4)["b"]
	fc.seed("k1", row)

	require.NoError(t, rowReaders[0].read(context.Background(), st))
}

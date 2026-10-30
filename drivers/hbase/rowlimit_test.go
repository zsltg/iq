package hbase

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// TestExecScanRowLimit pins that a row limit in the context stops `scan <table>`
// and closes the scanner, and that the scan reads no more rows than it needs.
func TestExecScanRowLimit(t *testing.T) {
	tests := []struct {
		name       string
		limit      int
		args       []string
		wantRows   int
		wantReads  int
		wantCloses int
	}{
		{"no limit reads all", 0, []string{"scan", "books"}, 5, 5, 0},
		{"limit stops early", 2, []string{"scan", "books"}, 2, 2, 1},
		{"limit above the result", 9, []string{"scan", "books"}, 5, 5, 0},
		{"smaller scan argument wins", 3, []string{"scan", "books", "1"}, 1, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClient()
			for _, k := range []string{"1", "2", "3", "4", "5"} {
				fc.seed(k, cell("cf", "n", k))
			}
			st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
			ctx := query.WithRowLimit(context.Background(), tt.limit)

			got, err := st.Query(ctx, tt.args)

			require.NoError(t, err)
			require.Len(t, got, tt.wantRows)
			require.Len(t, fc.scanners, 1)
			require.Equal(t, tt.wantReads, fc.scanners[0].i)
			require.Equal(t, tt.wantCloses, fc.scanners[0].closes)
		})
	}
}

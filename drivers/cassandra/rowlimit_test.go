package cassandra

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// fakeIter is a rowIter that serves n rows and counts the reads and the closes.
type fakeIter struct {
	n      int
	reads  int
	closed int
}

func (f *fakeIter) MapScan(m map[string]any) bool {
	if f.reads >= f.n {
		return false
	}
	f.reads++
	m["id"] = f.reads
	return true
}

func (f *fakeIter) Close() error {
	f.closed++
	return nil
}

func TestCollectRowsLimit(t *testing.T) {
	tests := []struct {
		name      string
		limit     int
		wantRows  int
		wantReads int
	}{
		{"no limit reads all", 0, 5, 5},
		{"limit stops early", 2, 2, 2},
		{"limit above the result", 9, 5, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Store{decimal: numfmt.DecimalAuto}
			iter := &fakeIter{n: 5}
			ctx := query.WithRowLimit(context.Background(), tt.limit)

			got, err := s.collectRows(ctx, iter)

			require.NoError(t, err)
			require.Len(t, got, tt.wantRows)
			require.Equal(t, tt.wantReads, iter.reads)
			require.Equal(t, 1, iter.closed)
		})
	}
}

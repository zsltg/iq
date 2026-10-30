package neo4j

import (
	"context"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestQueryRowLimit(t *testing.T) {
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
			recs := make([]*neo4j.Record, 5)
			for i := range recs {
				recs[i] = &neo4j.Record{Keys: []string{"v"}, Values: []any{int64(i)}}
			}
			s, drv, _ := newFakeStore(target{}, rows(recs...))
			ctx := query.WithRowLimit(context.Background(), tt.limit)

			got, err := s.Query(ctx, []string{"UNWIND range(1, 5) AS v RETURN v"})

			require.NoError(t, err)
			require.Len(t, got, tt.wantRows)
			require.Equal(t, tt.wantReads, drv.session.script[0].res.pos)
			require.Equal(t, 1, drv.session.closed)
		})
	}
}

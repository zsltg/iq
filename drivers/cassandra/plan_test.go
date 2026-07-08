package cassandra

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

func TestExplainPlan(t *testing.T) {
	tests := []struct {
		name       string
		keys       selector.KeySet
		pred       predicate.Node
		unbounded  bool
		wantOps    []string
		wantFilter map[string]any
	}{
		{
			name:    "bounded reads by primary key",
			keys:    selector.KeySet{Keys: []string{"1", "2"}},
			wantOps: []string{"SELECT by primary key: fetch 2 requested key(s)"},
		},
		{
			name: "streamable full scan",
			keys: selector.KeySet{Scan: true, Streamable: true},
			wantOps: []string{
				"SELECT *: full-table scan (every row read, filtered client-side)",
				"cursor streamed in pages of 100",
			},
		},
		{
			name: "streamable pushdown appends ALLOW FILTERING",
			keys: selector.KeySet{Scan: true, Streamable: true},
			pred: predicate.Eq{Path: []string{"author"}, Value: "Tolkien"},
			wantOps: []string{
				`SELECT * WHERE "author" = ? ALLOW FILTERING: server-side pre-filter, then the full jq re-runs client-side`,
				"cursor streamed in pages of 100",
			},
			wantFilter: map[string]any{"where": `"author" = ?`},
		},
		{
			name:      "unbounded materializes",
			keys:      selector.KeySet{Scan: true, Streamable: true},
			unbounded: true,
			wantOps: []string{
				"SELECT *: full-table scan (every row read, filtered client-side)",
				"whole result materialized in memory",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := ExplainPlan(tt.keys, tt.pred, tt.unbounded)
			require.Equal(t, tt.wantOps, plan.Ops)
			require.Equal(t, tt.wantFilter, plan.Filter)
		})
	}
}

func TestExplainWrite(t *testing.T) {
	require.Equal(t, []string{"INSERT: upsert by primary key"}, ExplainWrite(query.Upsert).Ops)
	require.Equal(t,
		[]string{"INSERT ... IF NOT EXISTS: insert new primary keys, skip existing"},
		ExplainWrite(query.InsertOnly).Ops)
}

func TestExplainClearDrop(t *testing.T) {
	require.Equal(t, []string{"TRUNCATE: empty the table, keep its schema"}, ExplainClear().Ops)
	plan, ok := ExplainDrop()
	require.True(t, ok)
	require.Equal(t, []string{"DROP TABLE: remove the table and its data"}, plan.Ops)
}

func TestExplainDelete(t *testing.T) {
	plan, ok := ExplainDelete()
	require.True(t, ok)
	require.Equal(t, []string{
		"pre-read the keys (accounting only, for present-vs-absent)",
		"DELETE FROM <table> WHERE <pk> = ? per key: remove the named rows",
	}, plan.Ops)
}

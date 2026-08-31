package hbase

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

func TestExplainPlan(t *testing.T) {
	t.Run("bounded get by key", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Keys: []string{"1", "2"}}, nil, false)
		require.Contains(t, plan.Ops[0], "Get by row key")
		require.Contains(t, plan.Ops[0], "2 requested key(s)")
		require.Nil(t, plan.Filter)
	})

	t.Run("filtered scan lists pushed columns", func(t *testing.T) {
		pred := predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.Contains(t, plan.Ops[0], "SingleColumnValueFilter")
		require.Contains(t, plan.Ops[0], cellKey("cf", "author"))
		require.Equal(t, map[string]any{"columns": []string{cellKey("cf", "author")}}, plan.Filter)
	})
}

func TestExplainWrite(t *testing.T) {
	require.Contains(t, ExplainWrite(query.Upsert).Ops[0], "Put: upsert")
	require.Contains(t, ExplainWrite(query.InsertOnly).Ops[0], "CheckAndPut")
}

func TestExplainClearDrop(t *testing.T) {
	require.Contains(t, ExplainClear().Ops[0], "Delete per row")
	plan, ok := ExplainDrop()
	require.True(t, ok)
	require.Contains(t, plan.Ops[0], "DeleteTable")
}

func TestExplainDelete(t *testing.T) {
	plan, ok := ExplainDelete()
	require.True(t, ok)
	// The exists-only pre-read supplies the present-vs-absent split; the whole-row
	// Delete removes the named rows.
	require.Contains(t, plan.Ops[0], "exists-only Get")
	require.Contains(t, plan.Ops[1], "Delete (whole row)")
}

func TestExplainPlanStreamingMode(t *testing.T) {
	tests := []struct {
		name       string
		streamable bool
		unbounded  bool
		want       string
	}{
		{
			name:       "a streamable bounded query is paged",
			streamable: true,
			want:       "cursor streamed in pages of 100",
		},
		{
			name:       "an unbounded query materializes",
			streamable: true,
			unbounded:  true,
			want:       "whole result materialized in memory",
		},
		{
			name: "a non-streamable query materializes",
			want: "whole result materialized in memory",
		},
		{
			name:      "neither streamable nor bounded materializes",
			unbounded: true,
			want:      "whole result materialized in memory",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: tt.streamable}, nil, tt.unbounded)
			require.Equal(t, []string{
				"Scan: full-table scan (every row read, filtered client-side)",
				tt.want,
			}, plan.Ops)
		})
	}
}

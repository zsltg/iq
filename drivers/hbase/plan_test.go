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

	t.Run("full scan streamed", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, false)
		require.Contains(t, plan.Ops[0], "full-table scan")
		require.Contains(t, plan.Ops[1], "streamed in pages")
		require.Nil(t, plan.Filter)
	})

	t.Run("filtered scan lists pushed columns", func(t *testing.T) {
		pred := predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.Contains(t, plan.Ops[0], "SingleColumnValueFilter")
		require.Contains(t, plan.Ops[0], cellKey("cf", "author"))
		require.Equal(t, map[string]any{"columns": []string{cellKey("cf", "author")}}, plan.Filter)
	})

	t.Run("unbounded materializes", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, true)
		require.Contains(t, plan.Ops[1], "materialized in memory")
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

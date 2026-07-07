package elasticsearch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

func TestExplainPlan(t *testing.T) {
	t.Run("bounded keys fetch via mget", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Keys: []string{"1", "2"}}, nil, false)
		require.Contains(t, plan.Ops[0], "_mget")
		require.Contains(t, plan.Ops[0], "2 requested id(s)")
		require.Equal(t, map[string]any{"ids": []string{"1", "2"}}, plan.Filter)
	})

	t.Run("streamable scan with no predicate is a full walk", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, false)
		require.Contains(t, plan.Ops[0], "search_after")
		require.Contains(t, plan.Ops[0], "full-index scan")
		require.Contains(t, plan.Ops[1], "batches of")
		require.Nil(t, plan.Filter)
	})

	t.Run("pushable predicate shows the bool pre-filter", func(t *testing.T) {
		pred := predicate.Eq{Path: []string{"author"}, Value: "Herbert"}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.Contains(t, plan.Ops[0], "server-side pre-filter")
		require.Equal(t, map[string]any{"term": map[string]any{"author": "Herbert"}}, plan.Filter)
	})

	t.Run("an or of equalities shows a bool should", func(t *testing.T) {
		pred := predicate.Or{
			predicate.Eq{Path: []string{"a"}, Value: "x"},
			predicate.Eq{Path: []string{"b"}, Value: "y"},
		}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.Equal(t, map[string]any{"bool": map[string]any{
			"should": []any{
				map[string]any{"term": map[string]any{"a": "x"}},
				map[string]any{"term": map[string]any{"b": "y"}},
			},
			"minimum_should_match": 1,
		}}, plan.Filter)
	})

	t.Run("an or with an unpushable branch falls back", func(t *testing.T) {
		pred := predicate.Or{
			predicate.Eq{Path: []string{"a"}, Value: "x"},
			predicate.Cmp{Path: []string{"n"}, Op: predicate.Gt, Value: 1.0},
		}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.Nil(t, plan.Filter)
	})

	t.Run("unpushable predicate falls back to a full walk", func(t *testing.T) {
		pred := predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.Contains(t, plan.Ops[0], "full-index scan")
		require.Nil(t, plan.Filter)
	})

	t.Run("unbounded materializes", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, true)
		require.Contains(t, plan.Ops[1], "materialized")
	})

	t.Run("holistic scan materializes", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: false}, nil, false)
		require.Contains(t, plan.Ops[1], "materialized")
	})
}

func TestExplainWrite(t *testing.T) {
	up := ExplainWrite(query.Upsert)
	require.Contains(t, up.Ops[0], "_bulk")
	require.Contains(t, up.Ops[0], "replace-upsert")

	ins := ExplainWrite(query.InsertOnly)
	require.Contains(t, ins.Ops[0], "create")
	require.Contains(t, ins.Ops[0], "skip existing")
}

func TestExplainClearDrop(t *testing.T) {
	clear := ExplainClear()
	require.Contains(t, clear.Ops[0], "_delete_by_query")
	require.Contains(t, strings.ToLower(clear.Ops[0]), "keep its mapping")

	drop, ok := ExplainDrop()
	require.True(t, ok)
	require.Contains(t, drop.Ops[0], "DELETE /{index}")
}

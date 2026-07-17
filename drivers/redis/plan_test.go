package redis

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/selector"
)

func TestExplainPlan(t *testing.T) {
	t.Run("bounded keys pipeline TYPE then typed reads", func(t *testing.T) {
		got := ExplainPlan(selector.KeySet{Keys: []string{"a", "b", "c"}}, nil, false)
		require.Nil(t, got.Filter)
		require.Equal(t, []string{
			"pipeline TYPE for 3 requested key(s)",
			"then per key by type: GET / HGETALL / LRANGE 0 -1 / SMEMBERS / ZRANGE 0 -1 WITHSCORES / JSON.GET",
		}, got.Ops)
	})

	t.Run("scan without a predicate reads every key client-side", func(t *testing.T) {
		got := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, false)
		require.Nil(t, got.Filter)
		require.Contains(t, got.Ops[0], "SCAN 0 MATCH * COUNT 100")
		require.Contains(t, got.Ops, "no server-side filter — every key is read and filtered client-side")
	})

	t.Run("scan with a predicate names the raw-byte prefilter and shows it", func(t *testing.T) {
		pred := predicate.Eq{Path: []string{"author", "name"}, Value: "Rob"}
		got := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.Contains(t, got.Ops[0], "SCAN 0 MATCH * COUNT 100")
		require.Contains(t, got.Ops[len(got.Ops)-1], "client-side raw-byte prefilter (rawpred)")
		require.NotContains(t, got.Ops, "no server-side filter — every key is read and filtered client-side")
		require.Equal(t, map[string]any{
			"eq": map[string]any{"field": "author.name", "value": "Rob"},
		}, got.Filter)
	})
}

func TestDescribePredicate(t *testing.T) {
	tests := []struct {
		name string
		pred predicate.Node
		want map[string]any
	}{
		{"eq", predicate.Eq{Path: []string{"a"}, Value: 5.0}, map[string]any{"eq": map[string]any{"field": "a", "value": 5.0}}},
		{"ne", predicate.Ne{Path: []string{"a"}, Value: "x"}, map[string]any{"ne": map[string]any{"field": "a", "value": "x"}}},
		{"cmp gt", predicate.Cmp{Path: []string{"a"}, Op: predicate.Gt, Value: 5.0}, map[string]any{"gt": map[string]any{"field": "a", "value": 5.0}}},
		{"cmp ge", predicate.Cmp{Path: []string{"a"}, Op: predicate.Ge, Value: 5.0}, map[string]any{"ge": map[string]any{"field": "a", "value": 5.0}}},
		{"cmp lt", predicate.Cmp{Path: []string{"a"}, Op: predicate.Lt, Value: 5.0}, map[string]any{"lt": map[string]any{"field": "a", "value": 5.0}}},
		{"cmp le", predicate.Cmp{Path: []string{"a"}, Op: predicate.Le, Value: 5.0}, map[string]any{"le": map[string]any{"field": "a", "value": 5.0}}},
		{"exists", predicate.Exists{Path: []string{"a", "b"}}, map[string]any{"exists": "a.b"}},
		{"notExists", predicate.NotExists{Path: []string{"a"}}, map[string]any{"notExists": "a"}},
		{"regex", predicate.Regex{Path: []string{"a"}, Pattern: "^x", Flags: "i"}, map[string]any{"regex": map[string]any{"path": "a", "pattern": "^x", "flags": "i"}}},
		{"size", predicate.Size{Path: []string{"a"}, N: 3}, map[string]any{"size": map[string]any{"path": "a", "length": 3}}},
		{
			"elemMatch",
			predicate.ElemMatch{Path: []string{"xs"}, Cond: predicate.Eq{Path: []string{"k"}, Value: 1.0}},
			map[string]any{"elemMatch": map[string]any{"path": "xs", "cond": map[string]any{"eq": map[string]any{"field": "k", "value": 1.0}}}},
		},
		{
			"noneMatch",
			predicate.NoneMatch{Path: []string{"xs"}, Cond: predicate.Eq{Path: []string{"k"}, Value: 1.0}},
			map[string]any{"noneMatch": map[string]any{"path": "xs", "cond": map[string]any{"eq": map[string]any{"field": "k", "value": 1.0}}}},
		},
		{
			"and",
			predicate.And{predicate.Exists{Path: []string{"a"}}, predicate.NotExists{Path: []string{"b"}}},
			map[string]any{"and": []map[string]any{{"exists": "a"}, {"notExists": "b"}}},
		},
		{
			"or",
			predicate.Or{predicate.Eq{Path: []string{"a"}, Value: 1.0}, predicate.Eq{Path: []string{"a"}, Value: 2.0}},
			map[string]any{"or": []map[string]any{{"eq": map[string]any{"field": "a", "value": 1.0}}, {"eq": map[string]any{"field": "a", "value": 2.0}}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, describePredicate(tt.pred))
		})
	}
}

func TestExplainDelete(t *testing.T) {
	got, ok := ExplainDelete()
	require.True(t, ok)
	require.Len(t, got.Ops, 1)
	require.Contains(t, got.Ops[0], "DEL key...")
}

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

	t.Run("an equality with a null literal shows no filter", func(t *testing.T) {
		// jq reads a missing field as null, so `== null` matches absent-or-null, which
		// no term query reproduces. The plan must show the full walk, not a term on null.
		pred := predicate.Eq{Path: []string{"a"}, Value: nil}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.Contains(t, plan.Ops[0], "full-index scan")
		require.Nil(t, plan.Filter)
	})

	t.Run("an equality on a nested path shows no filter", func(t *testing.T) {
		// Only a single top-level field can back a term, so a nested path falls back.
		pred := predicate.Eq{Path: []string{"a", "b"}, Value: "x"}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.Contains(t, plan.Ops[0], "full-index scan")
		require.Nil(t, plan.Filter)
	})

	t.Run("an empty or shows no filter", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, predicate.Or{}, false)
		require.Nil(t, plan.Filter)
	})

	t.Run("an or of one branch still shows a bool should", func(t *testing.T) {
		pred := predicate.Or{predicate.Eq{Path: []string{"a"}, Value: "x"}}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.Equal(t, map[string]any{"bool": map[string]any{
			"should":               []any{map[string]any{"term": map[string]any{"a": "x"}}},
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

func TestExplainDelete(t *testing.T) {
	del, ok := ExplainDelete()
	require.True(t, ok)
	require.Contains(t, del.Ops[0], "_bulk")
	require.Contains(t, del.Ops[0], "delete")
}

func TestExplainPlanStages(t *testing.T) {
	const prefilterScope = "rawpred on the raw _source of each hit, before decode"
	serverCond := query.StagePlan{
		State: query.StageConditional,
		Scope: "elasticsearch bool query",
		Note:  "each equality field is exactly mapped and its literal type matches",
	}
	serverOff := query.StagePlan{State: query.StageOff, Note: "no conjunct pushes"}
	prefilterCond := query.StagePlan{
		State: query.StageConditional,
		Scope: prefilterScope,
		Note:  "unless every compiled equality translates to an exact term under the live mapping",
	}
	prefilterOn := query.StagePlan{State: query.StageOn, Scope: prefilterScope}
	scan := selector.KeySet{Scan: true, Streamable: true}
	tests := []struct {
		name          string
		keys          selector.KeySet
		pred          predicate.Node
		wantServer    query.StagePlan
		wantPrefilter query.StagePlan
	}{
		{
			name:          "unmapped title equality",
			keys:          scan,
			pred:          predicate.Eq{Path: []string{"title"}, Value: "Dune"},
			wantServer:    serverCond,
			wantPrefilter: prefilterCond,
		},
		{
			name:          "mismatched literal type",
			keys:          scan,
			pred:          predicate.Eq{Path: []string{"year"}, Value: "1965"},
			wantServer:    serverCond,
			wantPrefilter: prefilterCond,
		},
		{
			name: "_id with a range turns the prefilter off",
			keys: scan,
			pred: predicate.And{
				predicate.Eq{Path: []string{"_id"}, Value: "1"},
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
			},
			wantServer:    serverCond,
			wantPrefilter: query.StagePlan{State: query.StageOff, Note: "the predicate reads _id, which the raw _source does not carry"},
		},
		{
			name:          "range alone",
			keys:          scan,
			pred:          predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
			wantServer:    serverOff,
			wantPrefilter: prefilterOn,
		},
		{
			name: "equality plus a range",
			keys: scan,
			pred: predicate.And{
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
			},
			wantServer:    serverCond,
			wantPrefilter: prefilterOn,
		},
		{
			name:          "nil predicate",
			keys:          scan,
			wantServer:    serverOff,
			wantPrefilter: query.StagePlan{State: query.StageOff, Note: "no compiled predicate"},
		},
		{
			name: "bounded read",
			keys: selector.KeySet{Keys: []string{"1"}},
			pred: predicate.Eq{Path: []string{"a"}, Value: "x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := ExplainPlan(tt.keys, tt.pred, false)
			require.Equal(t, tt.wantServer, plan.Server)
			require.Equal(t, tt.wantPrefilter, plan.Prefilter)
		})
	}
}

// TestPrefilterStageAgreesWithMatcher pins that the plan helper and the live scan
// decision agree on every static case: off means no matcher, on means a matcher. A
// conditional plan lets the live mapping choose either way.
func TestPrefilterStageAgreesWithMatcher(t *testing.T) {
	mapped := predicate.Eq{Path: []string{"author"}, Value: "Herbert"}
	tests := []struct {
		name string
		pred predicate.Node
		want query.StageState
	}{
		{"_id equality", predicate.Eq{Path: []string{"_id"}, Value: "1"}, query.StageOff},
		{"_id inside a range and", predicate.And{mapped, predicate.Cmp{Path: []string{"_id"}, Op: predicate.Gt, Value: "a"}}, query.StageOff},
		{"range", predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0}, query.StageOn},
		{"null equality", predicate.Eq{Path: []string{"author"}, Value: nil}, query.StageOn},
		{"nested equality", predicate.Eq{Path: []string{"meta", "author"}, Value: "x"}, query.StageOn},
		{"existence", predicate.Exists{Path: []string{"author"}}, query.StageOn},
		{"mapped equality plus a range", predicate.And{mapped, predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0}}, query.StageOn},
		{"empty and", predicate.And{}, query.StageOn},
		{"empty or", predicate.Or{}, query.StageOn},
		{"mapped equality", mapped, query.StageConditional},
		{"unmapped equality", predicate.Eq{Path: []string{"title"}, Value: "Dune"}, query.StageConditional},
		{"mismatched literal", predicate.Eq{Path: []string{"year"}, Value: "1965"}, query.StageConditional},
		{"or of equalities", predicate.Or{mapped, predicate.Eq{Path: []string{"year"}, Value: 1965.0}}, query.StageConditional},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, prefilterStage(tt.pred).State)
			matcher := testStore().prefilterMatcher(tt.pred)
			switch tt.want {
			case query.StageOff:
				require.Nil(t, matcher)
			case query.StageOn:
				require.NotNil(t, matcher)
			}
		})
	}
}

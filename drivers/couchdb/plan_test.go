package couchdb

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

func TestExplainPlan(t *testing.T) {
	t.Run("bounded fetch by keys", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: false, Keys: []string{"1", "2"}}, nil, false, numfmt.DecimalAuto)
		require.Len(t, plan.Ops, 1)
		require.Contains(t, plan.Ops[0], "fetch 2 requested key(s)")
		require.Equal(t, map[string]any{"keys": []string{"1", "2"}}, plan.Filter)
	})

	t.Run("full scan when nothing is pushed", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, false, numfmt.DecimalAuto)
		require.Contains(t, plan.Ops[0], "full-database scan")
		require.Nil(t, plan.Filter)
		require.Contains(t, plan.Ops[1], "streamed in batches")
	})

	t.Run("find with a pushed selector", func(t *testing.T) {
		pred := predicate.Eq{Path: []string{"a"}, Value: "x"}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false, numfmt.DecimalAuto)
		require.Contains(t, plan.Ops[0], "_find")
		require.Equal(t, map[string]any{"a": "x"}, plan.Filter)
	})

	t.Run("non-narrowing predicate stays a full scan", func(t *testing.T) {
		pred := predicate.Ne{Path: []string{"a"}, Value: "x"}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false, numfmt.DecimalAuto)
		require.Contains(t, plan.Ops[0], "full-database scan")
		require.Nil(t, plan.Filter)
	})

	t.Run("unbounded materializes", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, true, numfmt.DecimalAuto)
		require.Contains(t, plan.Ops[1], "materialized in memory")
	})

	t.Run("non-streamable materializes", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: false}, nil, false, numfmt.DecimalAuto)
		require.Contains(t, plan.Ops[1], "materialized in memory")
	})
}

func TestExplainWrite(t *testing.T) {
	require.Contains(t, ExplainWrite(query.Upsert).Ops[0], "replace-upsert")
	require.Contains(t, ExplainWrite(query.InsertOnly).Ops[0], "skip existing")
}

func TestExplainClearAndDrop(t *testing.T) {
	require.Contains(t, ExplainClear().Ops[0], "_deleted")
	plan, ok := ExplainDrop()
	require.True(t, ok)
	require.Contains(t, strings.ToUpper(plan.Ops[0]), "DELETE")
}

func TestExplainDelete(t *testing.T) {
	plan, ok := ExplainDelete()
	require.True(t, ok)
	ops := strings.Join(plan.Ops, " ")
	require.Contains(t, ops, "_bulk_docs", "the delete goes through _bulk_docs")
	require.Contains(t, ops, "_rev", "the current _rev is read first")
}

func TestExplainPlanStages(t *testing.T) {
	scan := selector.KeySet{Scan: true, Streamable: true}
	tests := []struct {
		name       string
		keys       selector.KeySet
		pred       predicate.Node
		wantServer query.StagePlan
	}{
		{name: "scan with a pushing fragment", keys: scan, pred: predicate.Eq{Path: []string{"a"}, Value: "x"}, wantServer: query.StagePlan{State: query.StageOn, Scope: "mango selector"}},
		{name: "scan with nothing pushable", keys: scan, pred: predicate.Ne{Path: []string{"a"}, Value: "x"}, wantServer: query.StagePlan{State: query.StageOff, Note: "no conjunct pushes"}},
		{name: "scan with a nil predicate", keys: scan, wantServer: query.StagePlan{State: query.StageOff, Note: "no conjunct pushes"}},
		{name: "bounded read", keys: selector.KeySet{Keys: []string{"a"}}, pred: predicate.Eq{Path: []string{"a"}, Value: "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := ExplainPlan(tt.keys, tt.pred, false, numfmt.DecimalAuto)
			require.Equal(t, tt.wantServer, plan.Server)
			require.Equal(t, query.StagePlan{}, plan.Prefilter)
		})
	}
}

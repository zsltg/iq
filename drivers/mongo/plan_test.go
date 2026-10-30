package mongo

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
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
		wantFilter map[string]any
		wantOps    []string
	}{
		{
			name:       "bounded keys fetch by _id",
			keys:       selector.KeySet{Keys: []string{"book:1", "book:2"}},
			wantFilter: map[string]any{"_id": map[string]any{"$in": []string{"book:1", "book:2"}}},
			wantOps:    []string{"find by _id: fetch 2 requested key(s)"},
		},
		{
			name: "streamable scan, no predicate, full scan",
			keys: selector.KeySet{Scan: true, Streamable: true},
			wantOps: []string{
				"find({}): full-collection scan (every document read, filtered client-side)",
				"cursor streamed in batches of 100",
			},
		},
		{
			name:       "streamable scan with pushed predicate",
			keys:       selector.KeySet{Scan: true, Streamable: true},
			pred:       predicate.Eq{Path: []string{"total"}, Value: float64(99)},
			wantFilter: map[string]any{"total": float64(99)},
			wantOps: []string{
				"find(<filter>): server-side pre-filter, then the full jq re-runs client-side",
				"cursor streamed in batches of 100",
			},
		},
		{
			name:      "unbounded materializes",
			keys:      selector.KeySet{Scan: true, Streamable: true},
			unbounded: true,
			wantOps: []string{
				"find({}): full-collection scan (every document read, filtered client-side)",
				"whole result materialized in memory",
			},
		},
		{
			name: "holistic scan materializes",
			keys: selector.KeySet{Scan: true, Streamable: false},
			wantOps: []string{
				"find({}): full-collection scan (every document read, filtered client-side)",
				"whole result materialized in memory",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExplainPlan(tt.keys, tt.pred, tt.unbounded, numfmt.DecimalNumber)
			require.Equal(t, tt.wantOps, got.Ops)
			require.Equal(t, tt.wantFilter, got.Filter)
		})
	}
}

func TestExplainPlanStages(t *testing.T) {
	scan := selector.KeySet{Scan: true, Streamable: true}
	tests := []struct {
		name       string
		keys       selector.KeySet
		pred       predicate.Node
		wantServer query.StagePlan
	}{
		{name: "scan with a pushing fragment", keys: scan, pred: predicate.Eq{Path: []string{"a"}, Value: "x"}, wantServer: query.StagePlan{State: query.StageOn, Scope: "mongo find"}},
		{name: "scan with a nil predicate", keys: scan, wantServer: query.StagePlan{State: query.StageOff, Note: "no conjunct pushes"}},
		{name: "bounded read", keys: selector.KeySet{Keys: []string{"a"}}, pred: predicate.Eq{Path: []string{"a"}, Value: "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := ExplainPlan(tt.keys, tt.pred, false, numfmt.DecimalNumber)
			require.Equal(t, tt.wantServer, plan.Server)
			require.Equal(t, query.StagePlan{}, plan.Prefilter)
		})
	}
}

func TestExplainPlanReadsTheDecimalMode(t *testing.T) {
	pred := predicate.Regex{Path: []string{"price"}, Pattern: "^9"}
	keys := selector.KeySet{Scan: true, Streamable: true}
	plain := map[string]any{"price": bson.M{"$regex": "^9"}}
	widened := map[string]any{"$or": bson.A{
		bson.M{"price": bson.M{"$regex": "^9"}},
		bson.M{"price": bson.M{"$type": "decimal"}},
	}}
	require.Equal(t, plain, ExplainPlan(keys, pred, false, numfmt.DecimalNumber).Filter)
	require.Equal(t, widened, ExplainPlan(keys, pred, false, numfmt.DecimalAuto).Filter)
	require.Equal(t, widened, ExplainPlan(keys, pred, false, numfmt.DecimalString).Filter)
}

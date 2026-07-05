package mongo

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
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
			got := ExplainPlan(tt.keys, tt.pred, tt.unbounded)
			require.Equal(t, tt.wantOps, got.Ops)
			require.Equal(t, tt.wantFilter, got.Filter)
		})
	}
}

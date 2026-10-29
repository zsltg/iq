package couchdb

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/selector"
)

// TestSelectorLeavesOutNodesThatHideDecodedNumbers pins that the selector never holds a
// leaf that compares a stored number where jq compares the decoded string. The server
// holds the number, so such a leaf can drop a document that jq keeps.
func TestSelectorLeavesOutNodesThatHideDecodedNumbers(t *testing.T) {
	price := []string{"price"}
	tests := []struct {
		name       string
		node       predicate.Node
		mode       numfmt.DecimalMode
		wantNarrow bool
	}{
		{"eq number text, string mode", predicate.Eq{Path: price, Value: "1.5"}, numfmt.DecimalString, false},
		{"eq number text, auto mode", predicate.Eq{Path: price, Value: "1.5"}, numfmt.DecimalAuto, true},
		{"eq out of range text, auto mode", predicate.Eq{Path: price, Value: "2e308"}, numfmt.DecimalAuto, false},
		{"eq plain text, string mode", predicate.Eq{Path: price, Value: "ada"}, numfmt.DecimalString, true},
		{"gt number, string mode", predicate.Cmp{Path: price, Op: predicate.Gt, Value: 1.0}, numfmt.DecimalString, false},
		{"gte string, string mode", predicate.Cmp{Path: price, Op: predicate.Ge, Value: "1"}, numfmt.DecimalString, false},
		{"gt number, auto mode", predicate.Cmp{Path: price, Op: predicate.Gt, Value: 1.0}, numfmt.DecimalAuto, true},
		{"lt string, string mode", predicate.Cmp{Path: price, Op: predicate.Lt, Value: "1"}, numfmt.DecimalString, true},
		{"regex, string mode", predicate.Regex{Path: price, Pattern: "^1"}, numfmt.DecimalString, false},
		{"regex, auto mode", predicate.Regex{Path: price, Pattern: "^1"}, numfmt.DecimalAuto, true},
		{"or with a hidden leaf", predicate.Or{
			predicate.Eq{Path: price, Value: "1.5"},
			predicate.Eq{Path: price, Value: "ada"},
		}, numfmt.DecimalString, false},
		{"and keeps the safe leaf", predicate.And{
			predicate.Eq{Path: price, Value: "1.5"},
			predicate.Eq{Path: []string{"name"}, Value: "ada"},
		}, numfmt.DecimalString, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, narrow := toSelector(tt.node, tt.mode)
			require.Equal(t, tt.wantNarrow, narrow)
			if tt.name == "and keeps the safe leaf" {
				require.Equal(t, map[string]any{"name": "ada"}, sel)
			}
		})
	}
}

func TestExplainPlanReadsTheDecimalMode(t *testing.T) {
	pred := predicate.Cmp{Path: []string{"price"}, Op: predicate.Gt, Value: 1.0}
	keys := selector.KeySet{Scan: true, Streamable: true}
	require.NotNil(t, ExplainPlan(keys, pred, false, numfmt.DecimalAuto).Filter)
	require.Nil(t, ExplainPlan(keys, pred, false, numfmt.DecimalString).Filter)
}

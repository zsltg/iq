package neo4j

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/selector"
)

// TestTranslateLeavesOutNodesThatHideDecodedNumbers pins that the WHERE never holds an
// equality with a number text. In string mode, the store turns a float property into
// that text. The server compares the float, so the equality can drop a node that jq keeps.
func TestTranslateLeavesOutNodesThatHideDecodedNumbers(t *testing.T) {
	price := []string{"price"}
	tests := []struct {
		name       string
		node       predicate.Node
		mode       numfmt.DecimalMode
		wantNarrow bool
	}{
		{"eq number text, string mode", predicate.Eq{Path: price, Value: "1.5"}, numfmt.DecimalString, false},
		{"eq number text, auto mode", predicate.Eq{Path: price, Value: "1.5"}, numfmt.DecimalAuto, true},
		{"eq plain text, string mode", predicate.Eq{Path: price, Value: "ada"}, numfmt.DecimalString, true},
		{"eq number, string mode", predicate.Eq{Path: price, Value: 1.5}, numfmt.DecimalString, true},
		{"or with a hidden leaf", predicate.Or{
			predicate.Eq{Path: price, Value: "1.5"},
			predicate.Eq{Path: price, Value: "ada"},
		}, numfmt.DecimalString, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &cypherBuilder{params: map[string]any{}, variable: "n", mode: tt.mode}
			_, narrow := b.translate(tt.node)
			require.Equal(t, tt.wantNarrow, narrow)
		})
	}
}

func TestExplainPlanReadsTheDecimalMode(t *testing.T) {
	pred := predicate.Eq{Path: []string{"price"}, Value: "1.5"}
	keys := selector.KeySet{Scan: true, Streamable: true}
	require.NotNil(t, ExplainPlan(keys, pred, false, numfmt.DecimalAuto).Filter)
	require.Nil(t, ExplainPlan(keys, pred, false, numfmt.DecimalString).Filter)
}

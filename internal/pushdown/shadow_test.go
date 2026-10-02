package pushdown_test

import (
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/pushdown"
)

// A user definition replaces a gojq builtin only when its name and arity both
// match. These rows pin the queries that still push while a definition sits in
// scope: a definition that shadows nothing pushdown matches must not turn
// pushdown off. A want of nil means the query must not push.
func TestCompileUserDefinitionShadowsBuiltin(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want predicate.Node
	}{
		{
			"unrelated name still pushes at the top",
			`def f: .; .[] | select(.a == 1)`,
			predicate.Eq{Path: []string{"a"}, Value: 1.0},
		},
		{
			"unrelated name still pushes in the select",
			`.[] | select(def f: .; .a == 1)`,
			predicate.Eq{Path: []string{"a"}, Value: 1.0},
		},
		{
			"same name with another arity still pushes",
			`def select(f; g): .; .[] | select(.a == 1)`,
			predicate.Eq{Path: []string{"a"}, Value: 1.0},
		},
		{
			"test/2 definition leaves a test/1 call alone",
			`def test(re; f): true; .[] | select(.a | test("x"))`,
			predicate.Regex{Path: []string{"a"}, Pattern: "x"},
		},
		{
			"has/2 definition leaves a has/1 call alone",
			`def has(k; v): true; .[] | select(.a | has("b"))`,
			predicate.Exists{Path: []string{"a", "b"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := gojq.Parse(tt.expr)
			require.NoError(t, err)

			got, ok := pushdown.Compile(q)

			if tt.want == nil {
				require.False(t, ok, "a shadowed builtin must not push, got %#v", got)
				return
			}
			require.True(t, ok, "expression should push")
			require.Equal(t, tt.want, got)
		})
	}
}

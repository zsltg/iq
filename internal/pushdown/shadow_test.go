package pushdown_test

import (
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/pushdown"
)

// A user definition with the name and arity of a builtin replaces that builtin
// in gojq. Pushdown matches select/1, not/0, has/1, test/1, test/2, any/1 and
// length/0 by name, so it must refuse a query where a definition in scope
// shadows one of them: the pushed predicate would drop records that the full
// query keeps. A want of nil means the query must not push.
func TestCompileUserDefinitionShadowsBuiltin(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want predicate.Node
	}{
		{"select/1 defined at the top", `def select(f): .; .[] | select(.a == 1)`, nil},
		{"select/1 defined in the stage", `.[] | def select(f): .; select(.a == 1)`, nil},
		{"not/0 defined at the top", `def not: true; .[] | select(.a == 1 | not)`, nil},
		{"not/0 defined in the select", `.[] | select(def not: true; .a == 1 | not)`, nil},
		{"not/0 defined in a parenthesis", `.[] | select((def not: true; .a == 1 | not))`, nil},
		{"has/1 defined at the top", `def has(k): true; .[] | select(.a | has("b"))`, nil},
		{"has/1 defined in the select", `.[] | select(def has(k): true; has("a"))`, nil},
		{"has/1 defined in a parenthesis", `.[] | select((def has(k): true; .a | has("b")))`, nil},
		{"has/1 defined on a bare call", `def has(k): true; .[] | select(has("a"))`, nil},
		{"test/1 defined at the top", `def test(re): true; .[] | select(.a | test("x"))`, nil},
		{"test/1 defined in the select", `.[] | select(def test(re): true; .a | test("x"))`, nil},
		{"test/2 defined at the top", `def test(re; f): true; .[] | select(.a | test("x"; "i"))`, nil},
		{"test/2 defined in a parenthesis", `.[] | select((def test(re; f): true; .a | test("x"; "i")))`, nil},
		{"any/1 defined at the top", `def any(f): true; .[] | select(.a | any(.b == 1))`, nil},
		{"any/1 defined in the select", `.[] | select(def any(f): true; .a | any(.b == 1))`, nil},
		{"length/0 defined at the top", `def length: 0; .[] | select(.a | length == 2)`, nil},
		{"length/0 defined in the select", `.[] | select(def length: 0; .a | length == 2)`, nil},
		{"length/0 defined in a parenthesis", `.[] | select((def length: 0; .a | length == 2))`, nil},
		{"shadow beside an unrelated definition", `def f: .; def select(f): .; .[] | select(.a == 1)`, nil},
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
		// The rule matches a definition to the matched builtins by name and arity
		// alone, not to the call it shadows, so it refuses a bit more than needed.
		{"test/2 defined beside a test/1 call", `def test(re; f): true; .[] | select(.a | test("x"))`, nil},
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

// Conjuncts feeds the explain breakdown, so it must agree with Compile: a
// shadowed select yields no breakdown, and a shadowed conjunct builtin is
// reported as not pushed.
func TestConjunctsUserDefinitionShadowsBuiltin(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"select/1 defined at the top", `def select(f): .; .[] | select(.a == 1)`},
		{"has/1 defined in the select", `.[] | select(def has(k): true; has("a"))`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := gojq.Parse(tt.expr)
			require.NoError(t, err)

			got, ok := pushdown.Conjuncts(q)

			if ok {
				for _, c := range got {
					require.Nil(t, c.Pred, "conjunct %q must not push", c.Expr)
				}
			}
		})
	}
}

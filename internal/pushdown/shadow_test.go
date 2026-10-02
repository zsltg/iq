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
		{"has/1 defined in a parenthesis before a not", `.[] | select((.a | def has(k): true; has("b")) | not)`, nil},
		{"select/1 defined in a middle stage", `.[] | def select(f): .; select(.a == 1) | .b`, nil},
		{"select/1 defined after the last select stage", `.[] | select(.a == 1) | .b, def select(f): .; .c`, nil},
		{"select/1 defined in a later operand of a stage", `.[] | select(.a == 1) | (.b | def select(f): .; select(.c))`, nil},
		{"has/1 defined before a trailing not", `.[] | select(.a | def has(k): true; has("b") | not)`, nil},
		{"not/0 defined before a trailing not", `.[] | select(.a | def not: true; has("b") | not)`, nil},
		{"any/1 defined before a trailing not", `.[] | select(.a | def any(f): true; any(.b == 1) | not)`, nil},
		{"has/1 defined inside an object value", `.[] | select(.a == 1) | {x: (def has(k): true; .b)}`, nil},
		{"select/1 defined inside a reduce", `.[] | select(.a == 1) | reduce .b[] as $i (0; def select(f): .; . + $i)`, nil},
		{"test/1 defined inside a call argument", `.[] | select(.a == 1) | map(def test(re): true; .b)`, nil},
		{"length/0 defined inside an if branch", `.[] | select(.a == 1) | if .b then (def length: 0; .c) else . end`, nil},
		{"has/1 defined inside an array", `.[] | select(.a == 1) | [def has(k): true; .b]`, nil},
		{"has/1 defined inside a try body", `.[] | select(.a == 1) | try (def has(k): true; .b)`, nil},
		{"has/1 defined inside a catch body", `.[] | select(.a == 1) | try .b catch (def has(k): true; .c)`, nil},
		{"has/1 defined inside a foreach update", `.[] | select(.a == 1) | foreach .b[] as $i (0; def has(k): true; . + $i)`, nil},
		{"has/1 defined inside a label", `.[] | select(.a == 1) | label $out | (def has(k): true; .b)`, nil},
		{"has/1 defined inside a string interpolation", `.[] | select(.a == 1) | "x\(def has(k): true; .b)"`, nil},
		{"has/1 defined inside a slice bound", `.[] | select(.a == 1) | .b[(def has(k): true; 1):]`, nil},
		{"has/1 defined inside an elif branch", `.[] | select(.a == 1) | if .b then 1 elif .c then (def has(k): true; 2) else 3 end`, nil},
		{"has/1 defined inside an object key query", `.[] | select(.a == 1) | {(def has(k): true; "x"): 1}`, nil},
		{"has/1 defined inside a pattern key query", `.[] | select(.a == 1) | . as {(def has(k): true; "x"): $v} | .b`, nil},
		{"has/1 defined inside a negated term", `.[] | select(.a == 1) | -(def has(k): true; 1)`, nil},
		{"has/1 defined in the second definition body", `def f: .; def g: (def has(k): true; .); .[] | select(.a == 1)`, nil},
		{"has/1 defined in the only definition body", `def f: (def has(k): true; .); .[] | select(.a == 1)`, nil},
		{"has/1 defined inside an object key string", `.[] | select(.a == 1) | {"x\(def has(k): true; .b)": 1}`, nil},
		{"has/1 defined inside a reduce pattern key", `.[] | select(.a == 1) | reduce .b[] as {(def has(k): true; "x"): $v} (0; .)`, nil},
		{"has/1 defined inside a foreach pattern key", `.[] | select(.a == 1) | foreach .b[] as {(def has(k): true; "x"): $v} (0; .)`, nil},
		{"has/1 defined inside an index key", `.[] | select(.a == 1) | .[(def has(k): true; "x")]`, nil},
		{"has/1 defined in a later array pattern element", `.[] | select(.a == 1) | . as [$a, {(def has(k): true; "x"): $v}] | .b`, nil},
		{"has/1 defined inside a pattern key string", `.[] | select(.a == 1) | . as {"x\(def has(k): true; "y")": $v} | .b`, nil},
		{"has/1 defined inside a nested pattern value", `.[] | select(.a == 1) | . as {a: {(def has(k): true; "x"): $v}} | .b`, nil},
		{"has/1 defined in an and operand", `.[] | select(.a == 1 and (def has(k): true; .b))`, nil},
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
// shadowing definition anywhere yields no breakdown, and a definition that
// shadows nothing leaves the conjunct pushed.
func TestConjunctsUserDefinitionShadowsBuiltin(t *testing.T) {
	tests := []struct {
		name   string
		expr   string
		want   []pushdown.Conjunct
		wantOK bool
	}{
		{"select/1 defined at the top", `def select(f): .; .[] | select(.a == 1)`, nil, false},
		{"has/1 defined in the select", `.[] | select(def has(k): true; has("a"))`, nil, false},
		{
			"has/1 defined in an and conjunct",
			`.[] | select(def has(k): true; has("a") and .b == 1)`,
			nil,
			false,
		},
		{
			"unrelated name still pushes",
			`def f: .; .[] | select(.a == 1)`,
			[]pushdown.Conjunct{{Expr: ".a == 1", Pred: predicate.Eq{Path: []string{"a"}, Value: 1.0}}},
			true,
		},
		{
			"no definition pushes",
			`.[] | select(.a == 1)`,
			[]pushdown.Conjunct{{Expr: ".a == 1", Pred: predicate.Eq{Path: []string{"a"}, Value: 1.0}}},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := gojq.Parse(tt.expr)
			require.NoError(t, err)

			got, ok := pushdown.Conjuncts(q)

			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

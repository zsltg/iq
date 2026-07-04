package pushdown_test

import (
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/pushdown"
)

func TestCompilePushable(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want predicate.Node
	}{
		{"single equality", ".[] | select(.a == 1)", predicate.Eq{Path: []string{"a"}, Value: 1.0}},
		{"string equality", `.[] | select(.a == "x")`, predicate.Eq{Path: []string{"a"}, Value: "x"}},
		{"literal on the left", ".[] | select(1 == .a)", predicate.Eq{Path: []string{"a"}, Value: 1.0}},
		{"nested path", ".[] | select(.a.b == 1)", predicate.Eq{Path: []string{"a", "b"}, Value: 1.0}},
		{"bracket path", `.[] | select(.["a"] == 1)`, predicate.Eq{Path: []string{"a"}, Value: 1.0}},
		{"bool literal", ".[] | select(.a == true)", predicate.Eq{Path: []string{"a"}, Value: true}},
		{"null literal", ".[] | select(.a == null)", predicate.Eq{Path: []string{"a"}, Value: nil}},
		{
			"and of equalities",
			`.[] | select(.a == "x" and .b == 2)`,
			predicate.And{
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Eq{Path: []string{"b"}, Value: 2.0},
			},
		},
		{
			"or of equalities",
			".[] | select(.a == 1 or .b == 2)",
			predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: 1.0},
				predicate.Eq{Path: []string{"b"}, Value: 2.0},
			},
		},
		{
			"partial and drops the negation (left compiles)",
			".[] | select(.a == 1 and .b != 2)",
			predicate.Eq{Path: []string{"a"}, Value: 1.0},
		},
		{
			"partial and drops the negation (right compiles)",
			".[] | select(.a != 2 and .b == 1)",
			predicate.Eq{Path: []string{"b"}, Value: 1.0},
		},
		{"greater than", ".[] | select(.year > 2015)", predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2015.0}},
		{"greater or equal", ".[] | select(.year >= 2015)", predicate.Cmp{Path: []string{"year"}, Op: predicate.Ge, Value: 2015.0}},
		{"less than", ".[] | select(.year < 2015)", predicate.Cmp{Path: []string{"year"}, Op: predicate.Lt, Value: 2015.0}},
		{"less or equal", ".[] | select(.year <= 2015)", predicate.Cmp{Path: []string{"year"}, Op: predicate.Le, Value: 2015.0}},
		{"string range", `.[] | select(.name > "m")`, predicate.Cmp{Path: []string{"name"}, Op: predicate.Gt, Value: "m"}},
		{"literal on left flips operator", ".[] | select(2015 < .year)", predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2015.0}},
		{"portable regex", `.[] | select(.name | test("^A"))`, predicate.Regex{Path: []string{"name"}, Pattern: "^A"}},
		{"regex with safe flags", `.[] | select(.name | test("^a"; "i"))`, predicate.Regex{Path: []string{"name"}, Pattern: "^a", Flags: "i"}},
		{"regex with shorthand class", `.[] | select(.code | test("^\\d+$"))`, predicate.Regex{Path: []string{"code"}, Pattern: `^\d+$`}},
		{
			"equality and parenthesized regex",
			`.[] | select(.type == "book" and (.name | test("^A")))`,
			predicate.And{
				predicate.Eq{Path: []string{"type"}, Value: "book"},
				predicate.Regex{Path: []string{"name"}, Pattern: "^A"},
			},
		},
		{
			"range and equality",
			`.[] | select(.year > 2015 and .type == "book")`,
			predicate.And{
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2015.0},
				predicate.Eq{Path: []string{"type"}, Value: "book"},
			},
		},
		{
			"two selects combine",
			".[] | select(.a == 1) | select(.b == 2)",
			predicate.And{
				predicate.Eq{Path: []string{"a"}, Value: 1.0},
				predicate.Eq{Path: []string{"b"}, Value: 2.0},
			},
		},
		{
			"select then projection",
			".[] | select(.a == 1) | .title",
			predicate.Eq{Path: []string{"a"}, Value: 1.0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := gojq.Parse(tt.expr)
			require.NoError(t, err)

			got, ok := pushdown.Compile(q)

			require.True(t, ok, "expression should push")
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCompileNotPushable(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"negation", ".[] | select(.a != 1)"},
		{"regex with lookahead", `.[] | select(.a | test("(?=x)"))`},
		{"regex with backreference", `.[] | select(.a | test("(a)\\1"))`},
		{"regex with unicode property", `.[] | select(.a | test("\\p{L}"))`},
		{"regex with unsafe flag", `.[] | select(.a | test("x"; "g"))`},
		{"regex pattern not a literal", `.[] | select(.a | test(.b))`},
		{"test on a computed input", `.[] | select((.a + "z") | test("x"))`},
		{"range against a bool literal", ".[] | select(.a > true)"},
		{"range against a null literal", ".[] | select(.a > null)"},
		{"and of two uncompilable", ".[] | select(.a != 1 and .b != 2)"},
		{"or with uncompilable right", ".[] | select(.a == 1 or .b != 2)"},
		{"or with uncompilable left", ".[] | select(.a != 1 or .b == 2)"},
		{"dotted field name is unsafe", `.[] | select(.["a.b"] == 1)`},
		{"truthy path", ".[] | select(.a)"},
		{"computed rhs", ".[] | select(.a == .b)"},
		{"no select", ".[] | .title"},
		{"bare iteration", ".[]"},
		{"not streamable (holistic)", "map(select(.a == 1))"},
		{"not streamable (keys)", "keys"},
		{"bounded, not a scan", `.["book:1"]`},
		{"iterating path atom", ".[] | select(.a[] == 1)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := gojq.Parse(tt.expr)
			require.NoError(t, err)

			_, ok := pushdown.Compile(q)

			require.False(t, ok, "expression should not push")
		})
	}
}

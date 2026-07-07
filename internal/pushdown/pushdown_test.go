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
			"partial and drops the uncompilable (left compiles)",
			".[] | select(.a == 1 and .b == .c)",
			predicate.Eq{Path: []string{"a"}, Value: 1.0},
		},
		{
			"partial and drops the uncompilable (right compiles)",
			".[] | select(.a == .c and .b == 1)",
			predicate.Eq{Path: []string{"b"}, Value: 1.0},
		},
		{"greater than", ".[] | select(.year > 2015)", predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2015.0}},
		{"greater or equal", ".[] | select(.year >= 2015)", predicate.Cmp{Path: []string{"year"}, Op: predicate.Ge, Value: 2015.0}},
		{"less than", ".[] | select(.year < 2015)", predicate.Cmp{Path: []string{"year"}, Op: predicate.Lt, Value: 2015.0}},
		{"less or equal", ".[] | select(.year <= 2015)", predicate.Cmp{Path: []string{"year"}, Op: predicate.Le, Value: 2015.0}},
		{"string range", `.[] | select(.name > "m")`, predicate.Cmp{Path: []string{"name"}, Op: predicate.Gt, Value: "m"}},
		{"literal on left flips operator", ".[] | select(2015 < .year)", predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2015.0}},
		{"has at the document root", `.[] | select(has("author"))`, predicate.Exists{Path: []string{"author"}}},
		{"has on a nested object", `.[] | select(.meta | has("isbn"))`, predicate.Exists{Path: []string{"meta", "isbn"}}},
		{"length equals", ".[] | select(.tags | length == 3)", predicate.Size{Path: []string{"tags"}, N: 3}},
		{"length equals zero", ".[] | select(.tags | length == 0)", predicate.Size{Path: []string{"tags"}, N: 0}},
		{"length equality reversed", ".[] | select(.tags | 3 == length)", predicate.Size{Path: []string{"tags"}, N: 3}},
		{
			"any with an equality",
			".[] | select(.items | any(.p == 6))",
			predicate.ElemMatch{Path: []string{"items"}, Cond: predicate.Eq{Path: []string{"p"}, Value: 6.0}},
		},
		{
			"any with a compound element condition",
			".[] | select(.items | any(.p > 5 and .q == 1))",
			predicate.ElemMatch{Path: []string{"items"}, Cond: predicate.And{
				predicate.Cmp{Path: []string{"p"}, Op: predicate.Gt, Value: 5.0},
				predicate.Eq{Path: []string{"q"}, Value: 1.0},
			}},
		},
		{"inequality", ".[] | select(.status != 1)", predicate.Ne{Path: []string{"status"}, Value: 1.0}},
		{"inequality with null", ".[] | select(.deleted != null)", predicate.Ne{Path: []string{"deleted"}, Value: nil}},
		{"negated equality", ".[] | select(.a == 1 | not)", predicate.Ne{Path: []string{"a"}, Value: 1.0}},
		{"negated has", `.[] | select(has("opt") | not)`, predicate.NotExists{Path: []string{"opt"}}},
		{
			"negated any (none match)",
			".[] | select(.items | any(.p == 6) | not)",
			predicate.NoneMatch{Path: []string{"items"}, Cond: predicate.Eq{Path: []string{"p"}, Value: 6.0}},
		},
		{
			"negated any with a compound equality",
			`.[] | select(.items | any(.sku == "x" and .active == true) | not)`,
			predicate.NoneMatch{Path: []string{"items"}, Cond: predicate.And{
				predicate.Eq{Path: []string{"sku"}, Value: "x"},
				predicate.Eq{Path: []string{"active"}, Value: true},
			}},
		},
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
		{"regex with lookahead", `.[] | select(.a | test("(?=x)"))`},
		{"regex with backreference", `.[] | select(.a | test("(a)\\1"))`},
		{"regex with unicode property", `.[] | select(.a | test("\\p{L}"))`},
		{"regex with unsafe flag", `.[] | select(.a | test("x"; "g"))`},
		{"regex pattern not a literal", `.[] | select(.a | test(.b))`},
		{"test on a computed input", `.[] | select((.a + "z") | test("x"))`},
		{"negated range is not exact", ".[] | select(.a > 5 | not)"},
		{"negated regex is not exact", `.[] | select((.a | test("x")) | not)`},
		{"negated size is not exact", ".[] | select((.a | length == 3) | not)"},
		{"negated any over a range is not exact", ".[] | select(.items | any(.p > 5) | not)"},
		{"negated widened and is not exact", ".[] | select((.a == 1 and .b == .c) | not)"},
		{"negated widened and, uncompilable first", ".[] | select((.a == .c and .b == 2) | not)"},
		{"negated any over a widened and is not exact", ".[] | select(.items | any(.a == 1 and .b == .c) | not)"},
		{"negated exact and is not pushed", ".[] | select((.a == 1 and .b == 2) | not)"},
		{"any with two arguments", ".[] | select(.items | any(.p > 5; .q == 1))"},
		{"all is not any", ".[] | select(.items | all(.p > 5))"},
		{"any over a scalar element", ".[] | select(.items | any(. > 5))"},
		{"any with an uncompilable condition", `.[] | select(.items | any(.p | test("(?=x)")))`},
		{"has with a computed key", ".[] | select(has(.k))"},
		{"has with a dotted key", `.[] | select(has("a.b"))`},
		{"length with a range", ".[] | select(.tags | length > 3)"},
		{"length against a negative", ".[] | select(.tags | length == -1)"},
		{"length against a non-integer", ".[] | select(.tags | length == 2.5)"},
		{"range against a bool literal", ".[] | select(.a > true)"},
		{"range against a null literal", ".[] | select(.a > null)"},
		{"and of two uncompilable", ".[] | select(.a == .x and .b == .y)"},
		{"or with uncompilable right", ".[] | select(.a == 1 or .b == .c)"},
		{"or with uncompilable left", ".[] | select(.a == .c or .b == 2)"},
		{"dotted field name is unsafe", `.[] | select(.["a.b"] == 1)`},
		{"dollar field name is unsafe", `.[] | select(.["$where"] == "sleep(1)")`},
		{"dollar field in a nested path is unsafe", `.[] | select(.meta.["$expr"] == 1)`},
		{"dollar field via has is unsafe", `.[] | select(has("$where"))`},
		{"dollar field via nested has is unsafe", `.[] | select(.meta | has("$or"))`},
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

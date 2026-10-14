package predicate

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestString(t *testing.T) {
	tests := []struct {
		name string
		node Node
		want string
	}{
		{"eq", Eq{Path: []string{"author", "name"}, Value: "Kleppmann"}, `author.name == "Kleppmann"`},
		{"ne", Ne{Path: []string{"status"}, Value: "draft"}, `status != "draft"`},
		{"gt", Cmp{Path: []string{"year"}, Op: Gt, Value: 2015.0}, `year > 2015`},
		{"ge", Cmp{Path: []string{"year"}, Op: Ge, Value: 2015.0}, `year >= 2015`},
		{"lt", Cmp{Path: []string{"year"}, Op: Lt, Value: 2015.0}, `year < 2015`},
		{"le", Cmp{Path: []string{"year"}, Op: Le, Value: 2015.0}, `year <= 2015`},
		{"unknown op", Cmp{Path: []string{"year"}, Op: Op(9), Value: 1.0}, `year Op(9) 1`},
		{"exists", Exists{Path: []string{"isbn"}}, `has(isbn)`},
		{"not exists", NotExists{Path: []string{"isbn"}}, `!has(isbn)`},
		{"size", Size{Path: []string{"tags"}, N: 3}, `len(tags) == 3`},
		{"regex no flags", Regex{Path: []string{"title"}, Pattern: "^go"}, `title =~ /^go/`},
		{"regex i", Regex{Path: []string{"title"}, Pattern: "^go", Flags: "i"}, `title =~ /^go/i`},
		{"regex m", Regex{Path: []string{"title"}, Pattern: "^go", Flags: "m"}, `title =~ /^go/m`},
		{"regex im", Regex{Path: []string{"title"}, Pattern: "^go", Flags: "im"}, `title =~ /^go/im`},
		{"regex slash", Regex{Path: []string{"p"}, Pattern: "a/b"}, `p =~ /a\/b/`},
		{"regex escaped slash", Regex{Path: []string{"p"}, Pattern: `a\/b`}, `p =~ /a\/b/`},
		{"regex escaped backslash then slash", Regex{Path: []string{"p"}, Pattern: `a\\/b`}, `p =~ /a\\\/b/`},
		{"regex trailing backslash", Regex{Path: []string{"p"}, Pattern: `a\`}, `p =~ /a\/`},
		{"quoted field name", Eq{Path: []string{"first name"}, Value: 1.0}, `"first name" == 1`},
		{"quoted nested name", Eq{Path: []string{"a", "b-c"}, Value: 1.0}, `a."b-c" == 1`},
		{"empty field name", Exists{Path: []string{""}}, `has("")`},
		{"digit first name", Exists{Path: []string{"1a"}}, `has("1a")`},
		{"underscore name", Exists{Path: []string{"_a1"}}, `has(_a1)`},
		{"empty path", Eq{Value: "go"}, `@ == "go"`},
		{"whole float", Eq{Path: []string{"y"}, Value: 2015.0}, `y == 2015`},
		{"fractional float", Eq{Path: []string{"y"}, Value: 1.5}, `y == 1.5`},
		{"large float", Eq{Path: []string{"y"}, Value: 1e21}, `y == 1e+21`},
		{"nan", Eq{Path: []string{"y"}, Value: math.NaN()}, `y == NaN`},
		{"string with quote", Eq{Path: []string{"s"}, Value: `a"b`}, `s == "a\"b"`},
		{"string with html", Eq{Path: []string{"s"}, Value: "a<b&c>"}, `s == "a<b&c>"`},
		{"null", Eq{Path: []string{"s"}, Value: nil}, `s == null`},
		{"bool", Ne{Path: []string{"s"}, Value: true}, `s != true`},
		{"elem match", ElemMatch{Path: []string{"tags"}, Cond: Eq{Value: "go"}}, `tags any(@ == "go")`},
		{"none match", NoneMatch{Path: []string{"tags"}, Cond: Eq{Value: "draft"}}, `tags none(@ == "draft")`},
		{"empty and in any", ElemMatch{Path: []string{"t"}, Cond: And{}}, `t any(true)`},
		{"empty or in none", NoneMatch{Path: []string{"t"}, Cond: Or{}}, `t none(false)`},
		{"nil cond", ElemMatch{Path: []string{"tags"}}, `tags any(true)`},
		{
			"mixed and in any",
			ElemMatch{Path: []string{"authors"}, Cond: And{
				Eq{Path: []string{"name"}, Value: "a"},
				Cmp{Path: []string{"year"}, Op: Gt, Value: 2000.0},
			}},
			`authors any(name == "a" and year > 2000)`,
		},
		{
			"mixed and or in any",
			ElemMatch{Path: []string{"authors"}, Cond: Or{
				And{Eq{Path: []string{"name"}, Value: "a"}, Eq{Path: []string{"x"}, Value: 1.0}},
				Eq{Path: []string{"name"}, Value: "b"},
			}},
			`authors any((name == "a" and x == 1) or name == "b")`,
		},
		{
			"nested any",
			ElemMatch{Path: []string{"a"}, Cond: ElemMatch{Path: []string{"b"}, Cond: Eq{Value: 1.0}}},
			`a any(b any(@ == 1))`,
		},
		{
			"none with or",
			NoneMatch{Path: []string{"tags"}, Cond: Or{Eq{Value: "a"}, Eq{Value: "b"}}},
			`tags none(@ == "a" or @ == "b")`,
		},
		{"and", And{Eq{Path: []string{"a"}, Value: 1.0}, Eq{Path: []string{"b"}, Value: 2.0}}, `(a == 1 and b == 2)`},
		{"or", Or{Eq{Path: []string{"a"}, Value: 1.0}, Eq{Path: []string{"c"}, Value: 2.0}}, `(a == 1 or c == 2)`},
		{"widened or", Or{Eq{Path: []string{"a"}, Value: 1.0}, Eq{Path: []string{"c"}, Value: 2.0}}, `(a == 1 or c == 2)`},
		{"single and", And{Eq{Path: []string{"a"}, Value: 1.0}}, `(a == 1)`},
		{"single or", Or{Eq{Path: []string{"a"}, Value: 1.0}}, `(a == 1)`},
		{"empty and", And{}, `(true)`},
		{"empty or", Or{}, `(false)`},
		{"nil child", And{nil}, `(true)`},
		{
			"nested composite",
			And{Or{Eq{Path: []string{"a"}, Value: 1.0}, Exists{Path: []string{"b"}}}, NotExists{Path: []string{"c"}}},
			`((a == 1 or has(b)) and !has(c))`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.node.String())
		})
	}
}

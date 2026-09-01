package selector_test

import (
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/selector"
)

func TestKeysBounded(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want []string
	}{
		{"single field", ".key1", []string{"key1"}},
		{"string index with colon", `.["book:1"]`, []string{"book:1"}},
		{"quoted dotted field", `."a.b"`, []string{"a.b"}},
		{"leading index then suffix", ".key1.name", []string{"key1"}},
		{"array of two fields", "[ .key1, .key2 ]", []string{"key1", "key2"}},
		{"array of field paths", "[ .key1.name, .key2.name ]", []string{"key1", "key2"}},
		{"pipe uses only left as key", ".key1 | .name", []string{"key1"}},
		{"comma feeds both root", ".a, .b", []string{"a", "b"}},
		{"arithmetic feeds both root", ".a + .b", []string{"a", "b"}},
		{"parens preserve root", "(.a, .b).name", []string{"a", "b"}},
		{"object values feed root", "{x: .a, y: .b}", []string{"a", "b"}},
		{"object shorthand implies same key", "{a, b}", []string{"a", "b"}},
		{"object string shorthand implies key", `{"foo"}`, []string{"foo"}},
		{"mixed shorthand and explicit value", "{a, y: .b}", []string{"a", "b"}},
		{"unary term", "-.a", []string{"a"}},
		{"interpolation queries", `"\(.a)-\(.b)"`, []string{"a", "b"}},
		{"duplicates collapse in order", "[ .a, .a.x, .a ]", []string{"a"}},
		{"plain literal has no key", `"hi"`, nil},
		{"empty array has no key", "[]", nil},
		{"variable shorthand is not a key", "{$x}", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := gojq.Parse(tt.expr)
			require.NoError(t, err)

			got := selector.Keys(q)

			require.False(t, got.Scan, "expression must be bounded, not a scan")
			require.False(t, got.Streamable, "a bounded expression is never a stream")
			require.Equal(t, tt.want, got.Keys)
		})
	}
}

func TestKeysScan(t *testing.T) {
	tests := []struct {
		name       string
		expr       string
		streamable bool
	}{
		{"root iteration", ".[]", true},
		{"iterate then filter", ".[] | select(.x)", true},
		{"iterate then index", ".[].name", true},
		{"iterate then pipe chain", ".[] | .a | ascii_upcase", true},
		{"optional iteration", ".[]?", true},
		{"bare identity", ".", false},
		{"recurse", "..", false},
		{"keys builtin", "keys", false},
		{"to_entries builtin", "to_entries", false},
		{"map builtin", "map(.x)", false},
		{"array collects iteration", "[ .[] ]", false},
		{"iteration in a comma", ".[], .a", false},
		{"function with an iteration suffix", "keys[]", false},
		{"recurse with an iteration suffix", "..[]", false},
		{"optional identity", ".?", false},
		{"bracketed operator expression", `.["a" + "b"]`, false},
		{"bracketed string carrying a func def", `.[def f: .; "book:1"]`, false},
		{"bracketed format string", `.[@text "book:1"]`, false},
		{"bracketed string with a suffix", `.["book:1"?]`, false},
		{"variable index", ".[$k]", false},
		{"computed index", ".[.a]", false},
		{"slice index", ".[1:2]", false},
		{"interpolated index", `.["\(.x)"]`, false},
		{"variable reference", "$x", false},
		{"control flow", "if .a then .b end", false},
		{"root binding feeds the body the root", ".[] as $x | .", false},
		{"root binding, body ignores the root", ".[] as $x | $x", false},
		{"binding inside the residual stays per-element", ".[] | .a as $x | {$x}", true},
		{"scan wins over explicit key", "[ ., .a ]", false},
		{"unresolvable inside array", "[ .a, keys ]", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := gojq.Parse(tt.expr)
			require.NoError(t, err)

			got := selector.Keys(q)

			require.True(t, got.Scan, "expression must classify as a scan")
			require.Equal(t, tt.streamable, got.Streamable, "streamable classification")
			require.Empty(t, got.Keys)
		})
	}
}

// bracket wraps q as the subscript of a leading bracketed index, `.[q]`, which is
// the only position constString is consulted from.
func bracket(q *gojq.Query) *gojq.Query {
	return &gojq.Query{Term: &gojq.Term{
		Type:  gojq.TermTypeIndex,
		Index: &gojq.Index{Start: q},
	}}
}

func TestKeysMalformedAST(t *testing.T) {
	str := func(s string) *gojq.Term {
		return &gojq.Term{Type: gojq.TermTypeString, Str: &gojq.String{Str: s}}
	}
	tests := []struct {
		name           string
		query          *gojq.Query
		wantScan       bool
		wantStreamable bool
		wantKeys       []string
	}{
		{
			name:     "array term carrying no array",
			query:    &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeArray}},
			wantScan: false,
			wantKeys: nil,
		},
		{
			// gojq.Parse accepts a source with no expression in it (" ", a bare
			// comment) and returns this shape: no term, no operands. FuzzMatch found
			// it as a nil dereference in the walk.
			name:     "empty query at the root",
			query:    &gojq.Query{},
			wantScan: true,
		},
		{
			name:     "bracketed subscript that is an empty query",
			query:    bracket(&gojq.Query{}),
			wantScan: true,
		},
		{
			name: "bracketed subscript carrying both a term and an operator",
			query: bracket(&gojq.Query{
				Term:  str("book:1"),
				Left:  &gojq.Query{Term: str("book")},
				Right: &gojq.Query{Term: str("1")},
				Op:    gojq.OpAdd,
			}),
			wantScan: true,
		},
		{
			name:     "bracketed subscript that is a string term with no literal",
			query:    bracket(&gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeString}}),
			wantScan: true,
		},
		{
			name: "iteration suffix that also carries an index",
			query: &gojq.Query{Term: &gojq.Term{
				Type: gojq.TermTypeIdentity,
				SuffixList: []*gojq.Suffix{
					{Iter: true, Index: &gojq.Index{Name: "a"}},
				},
			}},
			wantScan:       true,
			wantStreamable: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got selector.KeySet

			require.NotPanics(t, func() { got = selector.Keys(tt.query) })

			require.Equal(t, tt.wantScan, got.Scan, "scan classification")
			require.Equal(t, tt.wantStreamable, got.Streamable, "streamable classification")
			require.Equal(t, tt.wantKeys, got.Keys)
		})
	}
}

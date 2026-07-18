package pushdown

import (
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"
)

// wantConjunct is the expected shape of one enumerated conjunct: its jq source,
// whether the compiler produced a pushable predicate, and (for a declined one) a
// substring the reason must contain.
type wantConjunct struct {
	expr   string
	pushed bool
	reason string
}

func TestConjuncts(t *testing.T) {
	tests := []struct {
		name   string
		filter string
		ok     bool
		want   []wantConjunct
	}{
		{
			name:   "pushed range",
			filter: `.[] | select(.total > 99)`,
			ok:     true,
			want:   []wantConjunct{{expr: ".total > 99", pushed: true}},
		},
		{
			name:   "pushed equality and existence across two selects",
			filter: `.[] | select(.name == "x") | select(has("id"))`,
			ok:     true,
			want: []wantConjunct{
				{expr: `.name == "x"`, pushed: true},
				{expr: `has("id")`, pushed: true},
			},
		},
		{
			name:   "pushed portable regex",
			filter: `.[] | select(.name | test("^abc"))`,
			ok:     true,
			want:   []wantConjunct{{expr: `.name | test("^abc")`, pushed: true}},
		},
		{
			name:   "pushed element match",
			filter: `.[] | select(.tags | any(.k == "v"))`,
			ok:     true,
			want:   []wantConjunct{{expr: `.tags | any(.k == "v")`, pushed: true}},
		},
		{
			name:   "top-level or is a single conjunct",
			filter: `.[] | select(.a == 1 or .b == 2)`,
			ok:     true,
			want:   []wantConjunct{{expr: `.a == 1 or .b == 2`, pushed: true}},
		},
		{
			name:   "and splits into per-conjunct decisions",
			filter: `.[] | select(.total > 99 and (.active | not))`,
			ok:     true,
			want: []wantConjunct{
				{expr: ".total > 99", pushed: true},
				{expr: ".active | not", pushed: false, reason: "negation is not exactly expressible"},
			},
		},
		{
			name:   "declined negation",
			filter: `.[] | select(.active | not)`,
			ok:     true,
			want:   []wantConjunct{{expr: ".active | not", pushed: false, reason: "negation is not exactly expressible"}},
		},
		{
			name:   "declined non-portable regex",
			filter: `.[] | select(.name | test("(?i)abc"))`,
			ok:     true,
			want:   []wantConjunct{{expr: `.name | test("(?i)abc")`, pushed: false, reason: "regex is not portable across backends"}},
		},
		{
			name:   "declined dotted field is named unsafe",
			filter: `.[] | select(.["a.b"] == 5)`,
			ok:     true,
			want:   []wantConjunct{{expr: `.["a.b"] == 5`, pushed: false, reason: `field "a.b" is dotted or $-prefixed, unsafe to push`}},
		},
		{
			name:   "declined dollar-prefixed field is named unsafe",
			filter: `.[] | select(.["$gt"] == 5)`,
			ok:     true,
			want:   []wantConjunct{{expr: `.["$gt"] == 5`, pushed: false, reason: `field "$gt" is dotted or $-prefixed, unsafe to push`}},
		},
		{
			name:   "unsafe field named on the right-hand side of a comparison",
			filter: `.[] | select(5 == .["a.b"])`,
			ok:     true,
			want:   []wantConjunct{{expr: `5 == .["a.b"]`, pushed: false, reason: `field "a.b" is dotted or $-prefixed, unsafe to push`}},
		},
		{
			name:   "unsafe field named in a nested path suffix",
			filter: `.[] | select(.meta["$x"] == 5)`,
			ok:     true,
			want:   []wantConjunct{{expr: `.meta["$x"] == 5`, pushed: false, reason: `field "$x" is dotted or $-prefixed, unsafe to push`}},
		},
		{
			name:   "declined bare truthiness is generic",
			filter: `.[] | select(.active)`,
			ok:     true,
			want:   []wantConjunct{{expr: ".active", pushed: false, reason: "not a pushable comparison"}},
		},
		{
			name:   "non-streamable filter yields nothing",
			filter: `. | select(.a == 1)`,
			ok:     false,
		},
		{
			name:   "streamable filter without a select yields nothing",
			filter: `.[] | {id}`,
			ok:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := gojq.Parse(tt.filter)
			require.NoError(t, err)

			conjuncts, ok := Conjuncts(q)
			require.Equal(t, tt.ok, ok)
			require.Len(t, conjuncts, len(tt.want))
			for i, w := range tt.want {
				got := conjuncts[i]
				require.Equal(t, w.expr, got.Expr)
				if w.pushed {
					require.NotNil(t, got.Pred)
					require.Empty(t, got.Reason)
				} else {
					require.Nil(t, got.Pred)
					require.Contains(t, got.Reason, w.reason)
				}
			}
		})
	}
}

// wrapQuery returns q wrapped in depth parenthesizing query terms, the AST shape a
// nested `(((expr)))` parses to. It builds the nesting directly so a test can exceed
// maxNestDepth without relying on the jq parser's own recursion limit.
func wrapQuery(q *gojq.Query, depth int) *gojq.Query {
	for i := 0; i < depth; i++ {
		q = &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeQuery, Query: q}}
	}
	return q
}

func TestUnwrapIsDepthBounded(t *testing.T) {
	inner, err := gojq.Parse(".a == 1")
	require.NoError(t, err)

	// A wrapped expression within the cap unwraps to its exact inner node.
	got, ok := unwrap(wrapQuery(inner, 1))
	require.True(t, ok)
	require.Same(t, inner, got)

	// Nesting past the cap is declined rather than peeled forever — the guard that
	// stops a progress-removal mutant from spinning past the timeout. It returns in
	// at most maxNestDepth+1 iterations regardless of how deep the input goes.
	_, ok = unwrap(wrapQuery(inner, maxNestDepth+50))
	require.False(t, ok)

	// topConjuncts drops an over-nested conjunct: it contributes nothing rather than
	// hanging, and the client-side jq still filters it.
	require.Nil(t, topConjuncts(wrapQuery(inner, maxNestDepth+50)))
}

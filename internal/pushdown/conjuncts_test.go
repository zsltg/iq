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
			// A select after a stage that changes the element tests the derived
			// value, not the element, so it gives no conjunct.
			name:   "select after an element-changing stage gives no conjunct",
			filter: `.[] | .x | select(.a == 1)`,
			ok:     false,
		},
		{
			// A select before the element-changing stage still tests the element.
			name:   "select before an element-changing stage keeps its conjunct",
			filter: `.[] | select(.a == 1) | .x | select(.b == 2)`,
			ok:     true,
			want:   []wantConjunct{{expr: ".a == 1", pushed: true}},
		},
		{
			// An identity stage passes the element on unchanged.
			name:   "identity stage before a select keeps its conjunct",
			filter: `.[] | . | select(.a == 1)`,
			ok:     true,
			want:   []wantConjunct{{expr: ".a == 1", pushed: true}},
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
			// A bare `not` is a single-stage conjunct (no pipe): the negation reason
			// is gated on a `path | not` pipe of at least two stages, so a lone `not`
			// must fall through to the generic reason. This pins the `len(stages) >= 2`
			// threshold in declineReason — a decrementer or removal of it would
			// misreport this as "negation is not exactly expressible".
			name:   "declined bare not is generic, not a negation reason",
			filter: `.[] | select(not)`,
			ok:     true,
			want:   []wantConjunct{{expr: "not", pushed: false, reason: "not a pushable comparison"}},
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
	for range depth {
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

	// Boundary: nesting exactly at the cap still unwraps to the exact inner node.
	// This is the last depth the loop reaches, so a decrementer on the init (start
	// at 1) or a `<=`→`<` on the bound would stop one peel short and decline it.
	got, ok = unwrap(wrapQuery(inner, maxNestDepth))
	require.True(t, ok)
	require.Same(t, inner, got)

	// Boundary: one past the cap is declined rather than peeled forever — the guard
	// that stops a progress-removal mutant from spinning past the timeout. Testing
	// exactly cap+1 (not far past) also pins the init: an incrementer starting the
	// counter below 0 would grant one extra peel and wrongly accept this depth.
	_, ok = unwrap(wrapQuery(inner, maxNestDepth+1))
	require.False(t, ok)

	// topConjuncts drops an over-nested conjunct: it contributes nothing rather than
	// hanging, and the client-side jq still filters it.
	require.Nil(t, topConjuncts(wrapQuery(inner, maxNestDepth+1)))
}

// TestUnwrapStopsAtNonWrapper drives unwrap directly with hand-built ASTs where
// exactly one clause of its stop-guard is true, so each clause is load-bearing:
// removing or misjoining it would make unwrap peel a node it must return as-is
// (yielding a different node, or a nil-pointer panic on the next peel). Such
// shapes (an operator carrying a term, a term with no query) cannot come from the
// parser, so they are constructed rather than parsed.
func TestUnwrapStopsAtNonWrapper(t *testing.T) {
	inner, err := gojq.Parse(".a == 1")
	require.NoError(t, err)

	tests := []struct {
		name string
		in   *gojq.Query
	}{
		{
			// e.Op != 0: a query that carries an operator is not a bare wrapper,
			// even when a term is also present.
			name: "operator set",
			in:   &gojq.Query{Op: gojq.OpAnd, Term: &gojq.Term{Type: gojq.TermTypeQuery, Query: inner}},
		},
		{
			// e.Term == nil: nothing to peel; must stop before dereferencing Term.
			name: "term nil",
			in:   &gojq.Query{},
		},
		{
			// e.Term.Type != TermTypeQuery: a non-parenthesizing term is the inner
			// expression already.
			name: "term is not a query wrapper",
			in:   &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeIdentity}},
		},
		{
			// len(e.Term.SuffixList) != 0: `(expr).foo` carries a suffix, so the
			// wrapper is not transparent and must be returned whole.
			name: "query wrapper carries a suffix",
			in: &gojq.Query{Term: &gojq.Term{
				Type:       gojq.TermTypeQuery,
				Query:      inner,
				SuffixList: []*gojq.Suffix{{Index: &gojq.Index{Name: "foo"}}},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := unwrap(tt.in)
			require.True(t, ok)
			require.Same(t, tt.in, got)
		})
	}
}

// TestIsTestPipe drives isTestPipe directly. Its false-returning guard rejects any
// right-hand pipe stage that is not exactly a bare `test(...)` call; each clause of
// that guard is isolated by a hand-built shape so removing or misjoining one lets a
// non-test (or malformed) right side be misread as a portable-regex decline.
func TestIsTestPipe(t *testing.T) {
	leftPath, err := gojq.Parse(".name")
	require.NoError(t, err)

	pipe := func(rhs *gojq.Query) *gojq.Query {
		return &gojq.Query{Op: gojq.OpPipe, Left: leftPath, Right: rhs}
	}

	tests := []struct {
		name string
		in   *gojq.Query
		want bool
	}{
		{
			name: "real path | test pipe",
			in:   pipe(&gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeFunc, Func: &gojq.Func{Name: "test"}}}),
			want: true,
		},
		{
			name: "not a pipe at all",
			in:   &gojq.Query{Op: gojq.OpEq, Left: leftPath, Right: leftPath},
			want: false,
		},
		{
			// rhs.Op != 0: an operator on the right side is not a bare call.
			name: "rhs carries an operator",
			in:   pipe(&gojq.Query{Op: gojq.OpAnd, Term: &gojq.Term{Type: gojq.TermTypeFunc, Func: &gojq.Func{Name: "test"}}}),
			want: false,
		},
		{
			// rhs.Term == nil: must reject before dereferencing Term.Func.
			name: "rhs term nil",
			in:   pipe(&gojq.Query{}),
			want: false,
		},
		{
			// rhs.Term.Func == nil: a non-call right side (here a plain field) is
			// not a test, and reaching Func.Name would panic.
			name: "rhs term is not a call",
			in:   pipe(&gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeIdentity}}),
			want: false,
		},
		{
			// len(rhs.Term.SuffixList) != 0: `test(...) .x` is not a bare test call.
			name: "rhs call carries a suffix",
			in: pipe(&gojq.Query{Term: &gojq.Term{
				Type:       gojq.TermTypeFunc,
				Func:       &gojq.Func{Name: "test"},
				SuffixList: []*gojq.Suffix{{Index: &gojq.Index{Name: "x"}}},
			}}),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isTestPipe(tt.in))
		})
	}
}

// TestRawPath drives rawPath directly. It returns a plain relative field path or
// nil for anything with an operator, extra function defs, a non-index term, a
// missing index, an iterating suffix, or a computed suffix. Each guard clause is
// isolated by a hand-built shape so removing or misjoining one would either surface
// a path that must stay nil, or panic on a nil index.
func TestRawPath(t *testing.T) {
	tests := []struct {
		name string
		in   *gojq.Query
		want []string
	}{
		{
			name: "plain single field",
			in:   &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeIndex, Index: &gojq.Index{Name: "a"}}},
			want: []string{"a"},
		},
		{
			name: "plain two-level path",
			in: &gojq.Query{Term: &gojq.Term{
				Type:       gojq.TermTypeIndex,
				Index:      &gojq.Index{Name: "a"},
				SuffixList: []*gojq.Suffix{{Index: &gojq.Index{Name: "b"}}},
			}},
			want: []string{"a", "b"},
		},
		{
			// q.Op != 0: an operator at the top is not a plain path.
			name: "operator set",
			in:   &gojq.Query{Op: gojq.OpAnd, Term: &gojq.Term{Type: gojq.TermTypeIndex, Index: &gojq.Index{Name: "a"}}},
			want: nil,
		},
		{
			// q.Term == nil: must stop before dereferencing the term.
			name: "term nil",
			in:   &gojq.Query{},
			want: nil,
		},
		{
			// len(q.FuncDefs) != 0: a leading function definition is not a path.
			name: "function def present",
			in: &gojq.Query{
				FuncDefs: []*gojq.FuncDef{{Name: "f"}},
				Term:     &gojq.Term{Type: gojq.TermTypeIndex, Index: &gojq.Index{Name: "a"}},
			},
			want: nil,
		},
		{
			// t.Type != TermTypeIndex: a non-index term has no path, even if an
			// Index field happens to be populated.
			name: "term is not an index",
			in:   &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeIdentity, Index: &gojq.Index{Name: "a"}}},
			want: nil,
		},
		{
			// t.Index == nil: an index term with no index resolves nothing.
			name: "index term missing index",
			in:   &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeIndex, Index: nil}},
			want: nil,
		},
		{
			// s.Iter: an iterating suffix (`.a[]`) breaks the plain-path shape.
			name: "iterating suffix",
			in: &gojq.Query{Term: &gojq.Term{
				Type:       gojq.TermTypeIndex,
				Index:      &gojq.Index{Name: "a"},
				SuffixList: []*gojq.Suffix{{Iter: true, Index: &gojq.Index{Name: "b"}}},
			}},
			want: nil,
		},
		{
			// s.Index == nil: a suffix with no index resolves nothing.
			name: "suffix missing index",
			in: &gojq.Query{Term: &gojq.Term{
				Type:       gojq.TermTypeIndex,
				Index:      &gojq.Index{Name: "a"},
				SuffixList: []*gojq.Suffix{{Iter: false, Index: nil}},
			}},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, rawPath(tt.in))
		})
	}
}

package pushdown

import (
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
)

// The compiler's shape guards reject AST nodes the jq parser can produce (a
// builtin carrying a suffix, a two-argument select) and AST nodes only a
// programmatic caller can build (a query holding both an operator and a term, a
// term with no string payload). Compile cannot reach the second class, so these
// tests exercise the helpers directly: the guards are load-bearing — dropping one
// makes the compiler push a predicate the filter never asked for, or dereference
// a nil field — and a unit test is the only place their contract is observable.

// mustParse parses a jq expression, failing the test if it does not parse.
func mustParse(t *testing.T, src string) *gojq.Query {
	t.Helper()
	q, err := gojq.Parse(src)
	require.NoError(t, err)
	return q
}

// mustTerm parses a jq expression and returns its term, failing the test when
// the expression is not a single term.
func mustTerm(t *testing.T, src string) *gojq.Term {
	t.Helper()
	q := mustParse(t, src)
	require.NotNil(t, q.Term, "expression should be a single term: %s", src)
	return q.Term
}

func TestSelectArg(t *testing.T) {
	tests := []struct {
		name  string
		query func(t *testing.T) *gojq.Query
		want  string // the select argument's jq source, "" when not a select stage
	}{
		{
			"a bare select yields its argument",
			func(t *testing.T) *gojq.Query { return mustParse(t, "select(.a == 1)") },
			".a == 1",
		},
		{
			"an operator stage is not a select even when it carries a select term",
			// Only a programmatic caller can build a query holding both; the guard
			// keeps the operator authoritative over the term.
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{
					Op:    gojq.OpAnd,
					Left:  mustParse(t, ".a"),
					Right: mustParse(t, ".b"),
					Term:  mustTerm(t, "select(.a == 1)"),
				}
			},
			"",
		},
		{
			"a stage with no term is not a select",
			func(t *testing.T) *gojq.Query { return &gojq.Query{} },
			"",
		},
		{
			"a select carrying a suffix is not a bare select",
			func(t *testing.T) *gojq.Query { return mustParse(t, "select(.a == 1)[]") },
			"",
		},
		{
			"another one-argument builtin is not a select",
			func(t *testing.T) *gojq.Query { return mustParse(t, "map(.a)") },
			"",
		},
		{
			"a two-argument select is not the one-argument form",
			func(t *testing.T) *gojq.Query { return mustParse(t, "select(.a; .b)") },
			"",
		},
		{
			"a path stage carries no function",
			func(t *testing.T) *gojq.Query { return mustParse(t, ".a") },
			"",
		},
		{
			"a select that carries function definitions still yields its argument",
			// selectArg does not check FuncDefs. The definitions stay in scope for
			// the argument, and the client-side jq run keeps the result correct.
			func(t *testing.T) *gojq.Query {
				q := mustParse(t, "def f: .; select(.a == 1)")
				require.NotEmpty(t, q.FuncDefs)
				return q
			},
			".a == 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arg, ok := selectArg(tt.query(t))

			if tt.want == "" {
				require.False(t, ok, "stage should not be a select")
				require.Nil(t, arg)
				return
			}
			require.True(t, ok, "stage should be a select")
			require.Equal(t, tt.want, arg.String())
		})
	}
}

func TestExtractPredShapeGuards(t *testing.T) {
	tests := []struct {
		name  string
		query func(t *testing.T) *gojq.Query
		want  predicate.Node // nil means "not pushable"
	}{
		{
			"an expression with no term pushes nothing",
			func(t *testing.T) *gojq.Query { return &gojq.Query{} },
			nil,
		},
		{
			"an operator wins over a parenthesized term",
			// A query with both an operator and a term is unreachable from the
			// parser; the guard makes the operator authoritative rather than
			// silently compiling the term's inner expression instead.
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{
					Op:    gojq.OpEq,
					Left:  mustParse(t, ".a"),
					Right: mustParse(t, "1"),
					Term:  mustTerm(t, "(.b == 2)"),
				}
			},
			predicate.Eq{Path: []string{"a"}, Value: 1.0},
		},
		{
			"an operator wins over a builtin term",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{
					Op:    gojq.OpEq,
					Left:  mustParse(t, ".a"),
					Right: mustParse(t, "1"),
					Term:  mustTerm(t, `has("z")`),
				}
			},
			predicate.Eq{Path: []string{"a"}, Value: 1.0},
		},
		{
			"a parenthesized expression with a suffix is not unwrapped",
			func(t *testing.T) *gojq.Query { return mustParse(t, "(.a == 1)[]") },
			nil,
		},
		{
			"a builtin with a suffix is not a bare builtin",
			func(t *testing.T) *gojq.Query { return mustParse(t, `has("a")[]`) },
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractPred(tt.query(t))

			if tt.want == nil {
				require.False(t, ok, "expression should not push")
				require.Nil(t, got)
				return
			}
			require.True(t, ok, "expression should push")
			require.Equal(t, tt.want, got)
		})
	}
}

// TestExtractExactShapeGuards gives extractExact, the negation-safe extractor,
// the same shape guards as extractPred. A dropped guard there turns into a
// wrong negated predicate, which drops matching documents.
func TestExtractExactShapeGuards(t *testing.T) {
	tests := []struct {
		name  string
		query func(t *testing.T) *gojq.Query
		want  predicate.Node // nil means "not exact"
	}{
		{
			"an expression with no term is not exact",
			func(t *testing.T) *gojq.Query { return &gojq.Query{} },
			nil,
		},
		{
			"an operator wins over a parenthesized term",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{
					Op:    gojq.OpEq,
					Left:  mustParse(t, ".a"),
					Right: mustParse(t, "1"),
					Term:  mustTerm(t, "(.b == 2)"),
				}
			},
			predicate.Eq{Path: []string{"a"}, Value: 1.0},
		},
		{
			"an operator wins over a builtin term",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{
					Op:    gojq.OpEq,
					Left:  mustParse(t, ".a"),
					Right: mustParse(t, "1"),
					Term:  mustTerm(t, `has("z")`),
				}
			},
			predicate.Eq{Path: []string{"a"}, Value: 1.0},
		},
		{
			"a parenthesized expression with a suffix is not unwrapped",
			func(t *testing.T) *gojq.Query { return mustParse(t, "(.a == 1)[]") },
			nil,
		},
		{
			"a builtin with a suffix is not a bare builtin",
			func(t *testing.T) *gojq.Query { return mustParse(t, `has("a")[]`) },
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractExact(tt.query(t))

			if tt.want == nil {
				require.False(t, ok, "expression should not be exact")
				require.Nil(t, got)
				return
			}
			require.True(t, ok, "expression should be exact")
			require.Equal(t, tt.want, got)
		})
	}
}

// TestPipeShapeGuards drives pipeAtom and exactPipe with the right side of a
// `.a | rhs` pipe. Only a bare builtin with no suffix is read as a builtin. An
// operator query wins over the term it carries, and a query with no term or
// no function is not a builtin.
func TestPipeShapeGuards(t *testing.T) {
	tests := []struct {
		name string
		rhs  func(t *testing.T) *gojq.Query
		want predicate.Node // nil means "not pushable"
	}{
		{
			"a bare has builtin",
			func(t *testing.T) *gojq.Query { return mustParse(t, `has("b")`) },
			predicate.Exists{Path: []string{"a", "b"}},
		},
		{
			"a query with no term",
			func(t *testing.T) *gojq.Query { return &gojq.Query{} },
			nil,
		},
		{
			"an operator query carrying a has term",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{
					Op:    gojq.OpAnd,
					Left:  mustParse(t, ".x"),
					Right: mustParse(t, ".y"),
					Term:  mustTerm(t, `has("b")`),
				}
			},
			nil,
		},
		{
			"a path term carries no function",
			func(t *testing.T) *gojq.Query { return mustParse(t, ".b") },
			nil,
		},
		{
			"a has builtin with a suffix",
			func(t *testing.T) *gojq.Query { return mustParse(t, `has("b")[]`) },
			nil,
		},
	}
	extractors := []struct {
		name string
		fn   func(pathQ, rhs *gojq.Query) (predicate.Node, bool)
	}{
		{"pipeAtom", pipeAtom},
		{"exactPipe", exactPipe},
	}
	for _, ex := range extractors {
		for _, tt := range tests {
			t.Run(ex.name+": "+tt.name, func(t *testing.T) {
				got, ok := ex.fn(mustParse(t, ".a"), tt.rhs(t))

				if tt.want == nil {
					require.False(t, ok, "pipe should not push")
					require.Nil(t, got)
					return
				}
				require.True(t, ok, "pipe should push")
				require.Equal(t, tt.want, got)
			})
		}
	}
}

// TestPathOf pins the shape guards of pathOf. A path is pushed only when every
// part of it is a plain field index. Each row sets exactly one guard.
func TestPathOf(t *testing.T) {
	tests := []struct {
		name  string
		query func(t *testing.T) *gojq.Query
		want  []string // nil means "not a path"
	}{
		{"a nested field path", func(t *testing.T) *gojq.Query { return mustParse(t, ".a.b") }, []string{"a", "b"}},
		{"a query with no term", func(t *testing.T) *gojq.Query { return &gojq.Query{} }, nil},
		{
			"an operator query carrying a path term",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{
					Op:    gojq.OpEq,
					Left:  mustParse(t, ".x"),
					Right: mustParse(t, "1"),
					Term:  mustTerm(t, ".a"),
				}
			},
			nil,
		},
		{
			"a query carrying function definitions",
			func(t *testing.T) *gojq.Query {
				q := mustParse(t, ".a")
				q.FuncDefs = mustParse(t, "def f: 1; .").FuncDefs
				return q
			},
			nil,
		},
		{
			"a term that is not an index but carries one",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeIdentity, Index: &gojq.Index{Name: "a"}}}
			},
			nil,
		},
		{
			"an index term with no index",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeIndex}}
			},
			nil,
		},
		{
			"an iterating suffix that also carries an index",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{Term: &gojq.Term{
					Type:       gojq.TermTypeIndex,
					Index:      &gojq.Index{Name: "a"},
					SuffixList: []*gojq.Suffix{{Iter: true, Index: &gojq.Index{Name: "b"}}},
				}}
			},
			nil,
		},
		{"an optional suffix has no index", func(t *testing.T) *gojq.Query { return mustParse(t, ".a?") }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := pathOf(tt.query(t))

			require.Equal(t, tt.want != nil, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestIsNot(t *testing.T) {
	tests := []struct {
		name  string
		query func(t *testing.T) *gojq.Query
		want  bool
	}{
		{"the bare not builtin", func(t *testing.T) *gojq.Query { return mustParse(t, "not") }, true},
		{"a query with no term", func(t *testing.T) *gojq.Query { return &gojq.Query{} }, false},
		{"a path term carries no function", func(t *testing.T) *gojq.Query { return mustParse(t, ".a") }, false},
		{
			"an operator query carrying a not term",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{
					Op:    gojq.OpPipe,
					Left:  mustParse(t, ".a"),
					Right: mustParse(t, "not"),
					Term:  mustTerm(t, "not"),
				}
			},
			false,
		},
		{"another argument-less builtin", func(t *testing.T) *gojq.Query { return mustParse(t, "length") }, false},
		{"not with an argument", func(t *testing.T) *gojq.Query { return mustParse(t, `not("x")`) }, false},
		{"not with a suffix", func(t *testing.T) *gojq.Query { return mustParse(t, "not[]") }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isNot(tt.query(t)))
		})
	}
}

func TestIsLength(t *testing.T) {
	tests := []struct {
		name  string
		query func(t *testing.T) *gojq.Query
		want  bool
	}{
		{"the bare length builtin", func(t *testing.T) *gojq.Query { return mustParse(t, "length") }, true},
		{"a query with no term", func(t *testing.T) *gojq.Query { return &gojq.Query{} }, false},
		{"a path term carries no function", func(t *testing.T) *gojq.Query { return mustParse(t, ".a") }, false},
		{
			"an operator query carrying a length term",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{
					Op:    gojq.OpPipe,
					Left:  mustParse(t, ".a"),
					Right: mustParse(t, "length"),
					Term:  mustTerm(t, "length"),
				}
			},
			false,
		},
		{"another argument-less builtin", func(t *testing.T) *gojq.Query { return mustParse(t, "not") }, false},
		{"length with an argument", func(t *testing.T) *gojq.Query { return mustParse(t, `length("x")`) }, false},
		{"length with a suffix", func(t *testing.T) *gojq.Query { return mustParse(t, "length[]") }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isLength(tt.query(t)))
		})
	}
}

func TestIntLiteral(t *testing.T) {
	tests := []struct {
		name  string
		query func(t *testing.T) *gojq.Query
		wantN int
		wantK bool
	}{
		{"a non-negative integer", func(t *testing.T) *gojq.Query { return mustParse(t, "3") }, 3, true},
		{"zero", func(t *testing.T) *gojq.Query { return mustParse(t, "0") }, 0, true},
		{"a fractional number", func(t *testing.T) *gojq.Query { return mustParse(t, "2.5") }, 0, false},
		{
			"a negative integer",
			// jq spells -1 as a unary minus over 1, so only a programmatic caller
			// reaches the sign check; a negative size would be a predicate no
			// backend can satisfy.
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeNumber, Number: "-1"}}
			},
			0, false,
		},
		{"a non-number literal", func(t *testing.T) *gojq.Query { return mustParse(t, "true") }, 0, false},
		{"a path is not a literal", func(t *testing.T) *gojq.Query { return mustParse(t, ".a") }, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, ok := intLiteral(tt.query(t))

			require.Equal(t, tt.wantK, ok)
			require.Equal(t, tt.wantN, n)
		})
	}
}

func TestStringLit(t *testing.T) {
	tests := []struct {
		name  string
		query func(t *testing.T) *gojq.Query
		wantS string
		wantK bool
	}{
		{"a plain string literal", func(t *testing.T) *gojq.Query { return mustParse(t, `"x"`) }, "x", true},
		{
			"an operator query is not a literal",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{Op: gojq.OpEq, Left: mustParse(t, "1"), Right: mustParse(t, "2")}
			},
			"", false,
		},
		{
			"an operator query carrying a string term",
			// Unreachable from the parser: the guard keeps an operator query from
			// being read as the string it happens to carry.
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{
					Op:    gojq.OpEq,
					Left:  mustParse(t, "1"),
					Right: mustParse(t, "2"),
					Term:  mustTerm(t, `"x"`),
				}
			},
			"", false,
		},
		{"a query with no term", func(t *testing.T) *gojq.Query { return &gojq.Query{} }, "", false},
		{
			"a query carrying function definitions is not a literal",
			func(t *testing.T) *gojq.Query { return mustParse(t, `def f: 1; "x"`) },
			"", false,
		},
		{
			"a non-string term carrying a string payload",
			// The parser never fills Str on a number term; the type check is what
			// keeps a term's payload from being read as its value.
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{Term: &gojq.Term{
					Type:   gojq.TermTypeNumber,
					Number: "1",
					Str:    &gojq.String{Str: "x"},
				}}
			},
			"", false,
		},
		{
			"a string term with no payload",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeString}}
			},
			"", false,
		},
		{"a sliced string is not a literal", func(t *testing.T) *gojq.Query { return mustParse(t, `"x"[0:1]`) }, "", false},
		{"an interpolated string is not a literal", func(t *testing.T) *gojq.Query { return mustParse(t, `"a\(.b)"`) }, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, ok := stringLit(tt.query(t))

			require.Equal(t, tt.wantK, ok)
			require.Equal(t, tt.wantS, s)
		})
	}
}

func TestLiteralOf(t *testing.T) {
	tests := []struct {
		name  string
		query func(t *testing.T) *gojq.Query
		wantV any
		wantK bool
	}{
		{"a number", func(t *testing.T) *gojq.Query { return mustParse(t, "1") }, 1.0, true},
		{"a string", func(t *testing.T) *gojq.Query { return mustParse(t, `"x"`) }, "x", true},
		{"true", func(t *testing.T) *gojq.Query { return mustParse(t, "true") }, true, true},
		{"false", func(t *testing.T) *gojq.Query { return mustParse(t, "false") }, false, true},
		{"null", func(t *testing.T) *gojq.Query { return mustParse(t, "null") }, nil, true},
		{"an array is not a scalar", func(t *testing.T) *gojq.Query { return mustParse(t, "[1]") }, nil, false},
		{"a path is not a literal", func(t *testing.T) *gojq.Query { return mustParse(t, ".a") }, nil, false},
		{
			"an operator query carrying a number term",
			// Unreachable from the parser: the guard keeps an operator query from
			// being read as the literal it happens to carry.
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{
					Op:    gojq.OpEq,
					Left:  mustParse(t, ".a"),
					Right: mustParse(t, "1"),
					Term:  &gojq.Term{Type: gojq.TermTypeNumber, Number: "1"},
				}
			},
			nil, false,
		},
		{"a query with no term", func(t *testing.T) *gojq.Query { return &gojq.Query{} }, nil, false},
		{
			"a query carrying function definitions",
			func(t *testing.T) *gojq.Query { return mustParse(t, "def f: 1; 2") },
			nil, false,
		},
		{"a sliced string literal", func(t *testing.T) *gojq.Query { return mustParse(t, `"x"[0:1]`) }, nil, false},
		{
			"a string term with no payload",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeString}}
			},
			nil, false,
		},
		{"an interpolated string", func(t *testing.T) *gojq.Query { return mustParse(t, `"a\(.b)"`) }, nil, false},
		{
			"an unparsable number",
			func(t *testing.T) *gojq.Query {
				return &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeNumber, Number: "1e"}}
			},
			nil, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, ok := literalOf(tt.query(t))

			require.Equal(t, tt.wantK, ok)
			require.Equal(t, tt.wantV, v)
		})
	}
}

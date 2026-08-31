package jqfmt

import (
	"errors"
	"strings"
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"
)

// mustParse parses src or fails the test, the single-parse input ExplainQuery takes.
func mustParse(t *testing.T, src string) *gojq.Query {
	t.Helper()
	q, err := gojq.Parse(src)
	require.NoError(t, err)
	return q
}

// canonical returns a query's single-line canonical form, the semantic identity
// used to check that pretty-printing changed only whitespace.
func canonical(t *testing.T, src string) string {
	t.Helper()
	q, err := gojq.Parse(src)
	require.NoError(t, err)
	return q.String()
}

func TestFormatRoundTrip(t *testing.T) {
	// Each pretty-printed form must re-parse to the identical canonical AST, so
	// formatting is guaranteed to preserve meaning.
	srcs := []string{
		`.`,
		`.a`,
		`.a.b.c`,
		`.["book:1"]`,
		`.[]`,
		`.[] | select(.year > 2000)`,
		`.[] | select(.a == 1 or .a == 2) | {id, total}`,
		`[.a, .b, .c]`,
		`{a: .x, b: .y, c: (.z | length)}`,
		`{id}`,
		`map(select(.n > 0))`,
		`.items[] | select(.qty > 0) | .name`,
		`if .a then .b else .c end`,
		`if .a then .b elif .c then .d else .e end`,
		`try (.a | .b) catch "err"`,
		`reduce .[] as $x (0; . + $x)`,
		`foreach .[] as $x (0; . + $x; .)`,
		`.a as $x | $x + 1`,
		`[.[] | . * 2]`,
		`.foo | @base64`,
		`.a + .b - .c`,
		`(.a // .b) | .c`,
		`.a | keys | length`,
		`{"key with space": .v, other: .w}`,
	}
	for _, src := range srcs {
		t.Run(src, func(t *testing.T) {
			out, err := Format(src, false)
			require.NoError(t, err)
			require.Equal(t, canonical(t, src), canonical(t, out),
				"pretty output must re-parse to the same query\n--- pretty ---\n%s", out)
		})
	}
}

func TestFormatGolden(t *testing.T) {
	// Exact-output cases pin the break policy and indentation: an object with one
	// entry stays inline but a second entry breaks it; an array/reduce/select breaks
	// only when its body contains a pipe; if/try multi-line only when they must.
	tests := []struct {
		src, want string
	}{
		{`{id}`, "{ id }"},
		{`{id, total}`, "{\n  id,\n  total\n}"},
		{`[.a, .b]`, "[.a, .b]"},
		{`[.[] | .x]`, "[\n  .[]\n  | .x\n]"},
		{`[]`, "[]"},
		{`if .a then .b end`, "if .a then .b end"},
		{`if .a then .b else .c end`, "if .a\nthen .b\nelse .c\nend"},
		{`try .a catch .b`, "try .a catch .b"},
		{`try (.a|.b)`, "try\n  (\n    .a\n    | .b\n  )"},
		{`reduce .[] as $x (0; . + $x)`, "reduce .[] as $x (0; . + $x)"},
		{`reduce .[] as $x (0; .a|.b)`, "reduce .[] as $x (\n  0;\n  .a\n  | .b\n)"},
		{`select(.a|.b)`, "select(\n  .a\n  | .b\n)"},
		{`try .a catch (.b|.c)`, "try .a\ncatch\n  (\n    .b\n    | .c\n  )"},
		{`foreach .[] as $x (0; . + $x)`, "foreach .[] as $x (0; . + $x)"},
		{`foreach .[] as $x (0; .a|.b; .c)`, "foreach .[] as $x (\n  0;\n  .a\n  | .b;\n  .c\n)"},
		{`foreach .[] as $x (0; .a|.b)`, "foreach .[] as $x (\n  0;\n  .a\n  | .b\n)"},
		{`foreach .[] as $x ((.a|.b); 0)`, "foreach .[] as $x (\n  (\n    .a\n    | .b\n  );\n  0\n)"},
		{`foreach .[] as $x ((.a|.b); 0; .c)`, "foreach .[] as $x (\n  (\n    .a\n    | .b\n  );\n  0;\n  .c\n)"},
		{`if .a then (.b|.c) else .d end`, "if .a\nthen\n  (\n    .b\n    | .c\n  )\nelse .d\nend"},
		{`.a | @base64`, ".a\n| @base64"},
		{`.[] | select(.total > 99) | {id, total}`, ".[]\n| select(.total > 99)\n| {\n  id,\n  total\n}"},
		// A top-level comma composition renders inline, separator then one space.
		{`.a, .b`, ".a, .b"},
		// Each if clause alone decides the break: a breaking condition, a breaking
		// then-body, or the mere presence of an elif or else clause.
		{`if (.a|.b) then .c end`, "if (\n  .a\n  | .b\n)\nthen .c\nend"},
		{`if .a then (.b|.c) end`, "if .a\nthen\n  (\n    .b\n    | .c\n  )\nend"},
		{`if .a then .b elif .c then .d end`, "if .a\nthen .b\nelif .c\nthen .d\nend"},
		// A try with no catch keeps the catch clause out of the break decision.
		{`try .a`, "try .a"},
		// reduce/foreach break on any clause: the accumulator seed, the update, or
		// foreach's optional extract; a foreach whose three clauses are all simple
		// stays inline.
		{`reduce .[] as $x ((.a|.b); 0)`, "reduce .[] as $x (\n  (\n    .a\n    | .b\n  );\n  0\n)"},
		{`foreach .[] as $x (0; .a; (.b|.c))`, "foreach .[] as $x (\n  0;\n  .a;\n  (\n    .b\n    | .c\n  )\n)"},
		{`foreach .[] as $x (0; . + $x; .)`, "foreach .[] as $x (0; . + $x; .)"},
		// An inline foreach stays inline inside an array too: a present but simple
		// extract clause must not report a break to the enclosing form.
		{`[foreach .[] as $x (0; . + $x; .)]`, "[foreach .[] as $x (0; . + $x; .)]"},
		// A func-def inside an argument breaks that argument.
		{`select(def f: .; f)`, "select(\n  def f: .;\n  f\n)"},
		// A binary composition breaks when either side does.
		{`[(.a|.b) + .c]`, "[\n  (\n    .a\n    | .b\n  ) + .c\n]"},
		{`[.a + (.b|.c)]`, "[\n  .a + (\n    .b\n    | .c\n  )\n]"},
		// A one-entry object breaks only when its value or computed key breaks.
		{`{a: .x}`, "{ a: .x }"},
		{`{(.k): .v}`, "{ (.k): .v }"},
		{`{a: (.x|.y)}`, "{\n  a: (\n    .x\n    | .y\n  )\n}"},
		// A func-def's parameter list and a call's argument separator.
		{`def f(x): x; f(.a)`, "def f(x): x;\nf(.a)"},
		{`limit(3; .[])`, "limit(3; .[])"},
		// Every suffix of a descended term is re-emitted, in order.
		{`(.a | .b).c.d`, "(\n  .a\n  | .b\n).c.d"},
		// Only a call literally named source with two string literals gets the
		// nested-filter treatment; anything else takes the ordinary path.
		{`foo("a"; "b")`, `foo("a"; "b")`},
		{`source(.x; ".a")`, `source(.x; ".a")`},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			out, err := Format(tt.src, false)
			require.NoError(t, err)
			require.Equal(t, tt.want, out)
		})
	}
}

func TestFormatColorsPaired(t *testing.T) {
	// Every ANSI color open must have a matching reset, so the escape stream is
	// well-formed regardless of how tokens nest.
	out, err := Format(`.[] | select(.a == 1) | {id, total}`, true)
	require.NoError(t, err)
	resets := strings.Count(out, ansiReset)
	opens := strings.Count(out, "\x1b[") - resets
	require.Equal(t, resets, opens, "unbalanced color opens vs resets in:\n%q", out)
	require.Positive(t, opens, "colored output should contain colors")
	require.Contains(t, out, "\n", "colored output should still be multi-line")
}

func TestFormatColoredStripsToPlain(t *testing.T) {
	// Stripping every ANSI escape from the colored form yields the plain form, so
	// coloring never changes layout or text.
	src := `.[] | select(.year > 2000) | {id, year}`
	plain, err := Format(src, false)
	require.NoError(t, err)
	colored, err := Format(src, true)
	require.NoError(t, err)
	require.Equal(t, plain, stripANSI(colored))
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func TestFormatNestedSource(t *testing.T) {
	// A static source("name"; "<sub>") call unquotes and pretty-prints the nested jq
	// inline as its own indented block: the sub-filter's own line breaks are re-indented
	// under the call, the closing paren returns to the call's own indent, and a suffix
	// on the call still follows it. Exact output, so a dropped token or a dropped
	// re-indent is visible.
	tests := []struct {
		name, src, want string
	}{
		{
			"pipe sub-filter",
			`source("orders"; ".[] | select(.vip)")`,
			"source(\"orders\";\n  .[]\n  | select(.vip)\n)",
		},
		{
			"single-term sub-filter",
			`source("n"; ".a")`,
			"source(\"n\";\n  .a\n)",
		},
		{
			"suffix after the call",
			`source("n"; ".a").x`,
			"source(\"n\";\n  .a\n).x",
		},
		{
			"nested source indents one level deeper",
			`source("n"; "source(\"m\"; \".a\")")`,
			"source(\"n\";\n  source(\"m\";\n    .a\n  )\n)",
		},
		{
			"an unparseable sub-filter stays a plain argument",
			`source("n"; ".[ | broken")`,
			`source("n"; ".[ | broken")`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Format(tt.src, false)
			require.NoError(t, err)
			require.Equal(t, tt.want, out)
		})
	}
}

// paint wraps text in the color of its role, the exact escape pairing tok emits.
func paint(r role, text string) string { return ansi[r] + text + ansiReset }

func TestFormatNestedSourceColored(t *testing.T) {
	// The nested block and the source() call around it are colored by the same setting,
	// so the call's own name, parens, separator and quoted source name carry color too,
	// not just the sub-filter. Exact escape stream, token by token.
	out, err := Format(`source("n"; ".a")`, true)
	require.NoError(t, err)
	want := paint(roleFunc, "source") + paint(rolePunc, "(") + paint(roleString, `"n"`) +
		paint(rolePunc, ";") + "\n  " + paint(rolePath, ".a") + "\n" + paint(rolePunc, ")")
	require.Equal(t, want, out)
}

func TestFormatParseError(t *testing.T) {
	src := `.[ | broken`
	_, err := Format(src, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse expression")
	// The gojq failure is wrapped, not flattened into a string, so a caller can still
	// reach the cause through errors.Unwrap.
	_, raw := gojq.Parse(src)
	require.Error(t, raw)
	cause := errors.Unwrap(err)
	require.Error(t, cause, "the parse failure must stay unwrappable")
	require.Equal(t, raw.Error(), cause.Error())
}

func TestExplain(t *testing.T) {
	// Each filter breaks into ordered pipe stages; a recognized stage carries a
	// terse description, an unrecognized one carries "". Text is asserted structurally
	// in TestExplainStagesJoinToFormat, so here only the count and per-stage Desc bind.
	tests := []struct {
		name  string
		src   string
		descs []string
	}{
		{"identity", `.`, []string{"the whole input"}},
		{"field path", `.a.b`, []string{"field .a.b"}},
		{"iterate", `.[]`, []string{"each element"}},
		{"iterate a field", `.users[]`, []string{"each element of .users"}},
		{"recurse", `..`, []string{"recurse over all values"}},
		{
			"select then object",
			`.users[] | select(.age > 30) | {name, city: .addr.city}`,
			[]string{"each element of .users", "keep inputs where .age > 30", "build an object (name, city)"},
		},
		{"map sort add", `map(.x) | sort_by(.n) | add`, []string{"apply .x to each element", "sort by .n", "sum / concatenate"}},
		{"builtin table", `keys | length`, []string{"sorted keys", "length"}},
		{"unknown function", `frobnicate`, []string{"call frobnicate"}},
		{"array collect", `[.a, .b]`, []string{"collect into an array"}},
		{"top-level comma", `.a, .b`, []string{"emit multiple values"}},
		{"comparison", `.a == 1`, []string{"compare with =="}},
		{"conditional", `if .a then .b else .c end`, []string{"conditional"}},
		{"reduce", `reduce .[] as $x (0; . + $x)`, []string{"accumulate over .[]"}},
		{"as binding keeps the operand desc", `.a as $x | $x + 1`, []string{"field .a", "compute with +"}},
		{"leading def is its own unlabeled stage", `def f: .+1; .a | f`, []string{"", "field .a", "call f"}},
		{"has and contains", `has("k") | contains({a: 1})`, []string{`has key "k"`, "contains { a: 1 }"}},
		{"string literal is unrecognized", `"x"`, []string{""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stages := ExplainQuery(mustParse(t, tt.src), false)
			require.Len(t, stages, len(tt.descs))
			for i, want := range tt.descs {
				require.Equal(t, want, stages[i].Desc, "stage %d desc", i)
			}
		})
	}
}

func TestExplainStagesJoinToFormat(t *testing.T) {
	// The stage Texts are the exact per-stage pretty print, so joining a pipe chain's
	// stages with the printer's own "\n| " separator reproduces Format verbatim — the
	// stage split adds no rendering of its own, and each stage re-parses.
	srcs := []string{
		`.`,
		`.users[] | select(.age > 30) | {name, city: .addr.city}`,
		`.a as $x | $x + 1`,
		`map(.x) | sort_by(.n) | add`,
		`if .a then .b else .c end`,
		`.items[] | select(.qty > 0) | .name`,
	}
	for _, src := range srcs {
		t.Run(src, func(t *testing.T) {
			stages := ExplainQuery(mustParse(t, src), false)
			texts := make([]string, len(stages))
			for i, s := range stages {
				texts[i] = s.Text
			}
			whole, err := Format(src, false)
			require.NoError(t, err)
			require.Equal(t, whole, strings.Join(texts, "\n| "))
		})
	}
}

func TestExplainPeelsLeadingDeclarations(t *testing.T) {
	// Module + imports + func-defs collapse into stage 0 verbatim; the body then keeps
	// only its non-declaration content. A single-query body makes that split observable:
	// if any declaration field leaked into the body it would re-render here, so this
	// pins both the decls text and the field clearing.
	stages := ExplainQuery(mustParse(t, `module {v: 1}; import "m" as m; def f: .+1; .a`), false)
	require.Len(t, stages, 2)
	require.Equal(t, "module { v: 1 };\nimport \"m\" as m;\ndef f: . + 1;", stages[0].Text)
	require.Empty(t, stages[0].Desc)
	require.Equal(t, ".a", stages[1].Text)
	require.Equal(t, "field .a", stages[1].Desc)
}

func TestHasLeadingDecls(t *testing.T) {
	// Each declaration kind alone makes ExplainQuery peel a leading stage, so a query
	// with only a module, only an import, or only a func-def still splits into two
	// stages; a plain filter stays one. This pins every operand of the predicate.
	tests := []struct {
		name       string
		src        string
		wantStages int
	}{
		{"module only", `module {v: 1}; .a`, 2},
		{"import only", `import "m" as m; .a`, 2},
		{"func-def only", `def f: .; .a`, 2},
		{"no declarations", `.a`, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := mustParse(t, tt.src)
			require.Equal(t, tt.wantStages == 2, HasLeadingDecls(q))
			require.Len(t, ExplainQuery(q, false), tt.wantStages)
		})
	}
}

func TestExplainEmptyObject(t *testing.T) {
	// An empty object has no keys, so its description is the bare form, never the
	// parenthesized key list — this pins the len(keys)==0 branch.
	stages := ExplainQuery(mustParse(t, `{}`), false)
	require.Len(t, stages, 1)
	require.Equal(t, "build an object", stages[0].Desc)
}

func TestVisibleWidth(t *testing.T) {
	// The visible width ignores this package's ANSI escapes, so a colored stage and
	// its plain form measure identically; a multi-byte rune counts as one column.
	src := `.[] | select(.a == 1) | {id, total}`
	plain, err := Format(src, false)
	require.NoError(t, err)
	colored, err := Format(src, true)
	require.NoError(t, err)
	require.NotEqual(t, plain, colored, "colored form should carry escapes")
	require.Equal(t, VisibleWidth(plain), VisibleWidth(colored))
	require.Equal(t, len([]rune(plain)), VisibleWidth(plain))
	require.Equal(t, 3, VisibleWidth("a—b"), "a multi-byte em dash is one column")
}

func TestVisibleWidthBoundaries(t *testing.T) {
	// The escape-skipping loop is exercised at its boundaries so an off-by-one in the
	// index math changes the count: a complete escape is skipped whole, a truncated one
	// (no '[', no terminating 'm', or a lone trailing ESC) is not, and its bytes count.
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"complete escape wraps text", "\x1b[31mab", 2}, // "\x1b[31m" skipped, "ab"→2
		{"only a complete escape", "\x1b[0m", 0},
		{"empty escape", "\x1b[m", 0},                // "\x1b[m" (no params) skipped whole
		{"trailing lone esc counts", "a\x1b", 2},     // ESC has no next byte to peek; counts
		{"esc without bracket", "\x1b3", 2},          // not a real escape; both bytes count
		{"unterminated escape counts", "\x1b[31", 4}, // no 'm'; every byte counts
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, VisibleWidth(tt.in))
		})
	}
}

func TestStringLiteral(t *testing.T) {
	// Only a plain, uninterpolated, unsuffixed string literal standing alone is a static
	// source() argument; every other query shape is rejected so the ordinary printing
	// path keeps it verbatim.
	tests := []struct {
		name, src, want string
		ok              bool
	}{
		{name: "plain literal", src: `"hello"`, want: "hello", ok: true},
		{name: "path term", src: `.a`},
		{name: "pipe composition", src: `.a | .b`},
		{name: "binary composition", src: `"a" + "b"`},
		{name: "func-def before the literal", src: `def f: .; "x"`},
		{name: "literal with a suffix", src: `"abc"?`},
		{name: "interpolated literal", src: `"\(.a)"`},
		{name: "format applied to a literal", src: `@base64 "text"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := stringLiteral(mustParse(t, tt.src))
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestStringLiteralRejectsMalformedQueries(t *testing.T) {
	// The parser never emits these shapes — it sets Op only on a composition, whose Term
	// is nil, and always fills Str on a string term — so each guard is pinned here on a
	// hand-built query. Rejection, not a nil dereference, is the contract.
	strTerm := &gojq.Term{Type: gojq.TermTypeString, Str: &gojq.String{Str: "x"}}
	tests := []struct {
		name string
		q    *gojq.Query
	}{
		{"no operator and no term", &gojq.Query{}},
		{"operator set beside a term", &gojq.Query{Op: gojq.OpAdd, Term: strTerm}},
		{"string term with no string", &gojq.Query{Term: &gojq.Term{Type: gojq.TermTypeString}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := stringLiteral(tt.q)
			require.False(t, ok)
			require.Empty(t, got)
		})
	}
}

func TestSourceCallRejectsMalformedTerms(t *testing.T) {
	// The shape check reads t.Func only after the term type says it is a call, and only
	// a call actually named source is rewritten. Each guard is pinned on a hand-built
	// term the parser would never produce, so none of them can be dropped silently.
	args := []*gojq.Query{mustParse(t, `"orders"`), mustParse(t, `".a"`)}
	tests := []struct {
		name string
		term *gojq.Term
	}{
		{"call payload on a non-call term", &gojq.Term{
			Type: gojq.TermTypeString,
			Str:  &gojq.String{Str: "x"},
			Func: &gojq.Func{Name: "source", Args: args},
		}},
		{"call term with no call payload", &gojq.Term{Type: gojq.TermTypeFunc}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &printer{}
			got, ok := p.sourceCall(tt.term)
			require.False(t, ok)
			require.Empty(t, got)
		})
	}
}

func TestTokColorsOnlyMappedRoles(t *testing.T) {
	// A role with no entry in the ansi table writes its text plain even with coloring on,
	// so the printer never opens a color it cannot name; a mapped role is wrapped in its
	// code and a reset.
	unmapped := role(len(ansi))
	tests := []struct {
		name    string
		colored bool
		r       role
		want    string
	}{
		{"mapped role colored", true, roleFunc, ansi[roleFunc] + "x" + ansiReset},
		{"unmapped role colored", true, unmapped, "x"},
		{"mapped role uncolored", false, roleFunc, "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Empty(t, ansi[unmapped], "the unmapped role must stay absent from the table")
			p := &printer{colored: tt.colored}
			p.tok(tt.r, "x")
			require.Equal(t, tt.want, p.b.String())
		})
	}
}

func TestQueryNeedsBreakOnDeclarations(t *testing.T) {
	// A leading declaration breaks its query on its own, whatever the body is. Only the
	// top-level query can carry a module header, and Format prints that one without
	// consulting the predicate, so the predicate is exercised directly here.
	tests := []struct {
		name, src string
		want      bool
	}{
		{"module header", `module {v: 1}; .a`, true},
		{"func-def", `def f: .; .a`, true},
		{"plain leaf body", `.a`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, queryNeedsBreak(mustParse(t, tt.src)))
		})
	}
}

func TestPrinterEmptyContainers(t *testing.T) {
	// An empty literal stays on one line and opens no indented block. termNeedsBreak
	// keeps an empty object or array on the leaf path, so these forms are pinned by
	// calling the descending printer directly.
	tests := []struct {
		name  string
		write func(p *printer)
		want  string
	}{
		{"empty object", func(p *printer) { p.object(&gojq.Object{}) }, "{}"},
		{"empty array", func(p *printer) { p.array(&gojq.Array{}) }, "[]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &printer{}
			tt.write(p)
			require.Equal(t, tt.want, p.b.String())
		})
	}
}

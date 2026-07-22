package jqfmt

import (
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
	// A static source("name"; "<sub>") call unquotes and pretty-prints the nested
	// jq inline as its own indented block.
	out, err := Format(`source("orders"; ".[] | select(.vip)")`, false)
	require.NoError(t, err)
	require.Contains(t, out, `source("orders";`)
	require.Contains(t, out, ".[]")
	require.Contains(t, out, "select(.vip)")
	// The nested sub-filter's pipe is broken onto its own line and indented.
	require.Contains(t, out, "\n  .[]")
	require.Contains(t, out, "\n  | select(.vip)")
}

func TestFormatParseError(t *testing.T) {
	_, err := Format(`.[ | broken`, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse expression")
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

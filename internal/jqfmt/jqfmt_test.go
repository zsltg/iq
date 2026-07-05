package jqfmt

import (
	"strings"
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"
)

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

package jqfmt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// maxFuzzInput bounds one fuzz input. A larger input tells the printer nothing new
// and it makes the -fuzztime budget dishonest.
const maxFuzzInput = 64 << 10

// FuzzFormat drives the pretty-printer with arbitrary jq source. Two oracles hold
// for every source gojq accepts. The printer is idempotent and its output re-parses:
// Format over the formatted text must succeed and give the same text again. The
// colored output differs from the plain output only in ANSI SGR escapes, so it
// must be identical after the escapes are removed.
func FuzzFormat(f *testing.F) {
	// Seeds from the jqfmt_test.go tables, plus hostile shapes: empty source, deep
	// nesting, invalid UTF-8 and a huge number literal.
	seeds := []string{
		`.`,
		`.a`,
		`.["book:1"]`,
		`.[]`,
		`.[] | select(.year > 2000)`,
		`.[] | select(.a == 1 or .a == 2) | {id, total}`,
		`{a: .x, b: .y, c: (.z | length)}`,
		`{id, total}`,
		`map(select(.n > 0))`,
		`if .a then .b elif .c then .d else .e end`,
		`try (.a | .b) catch "err"`,
		`reduce .[] as $x (0; . + $x)`,
		`foreach .[] as $x (0; . + $x; .)`,
		`[.[] | . * 2]`,
		`.foo | @base64`,
		`{"key with space": .v, other: .w}`,
		`def f: .; f`,
		``,
		`((((((((((.))))))))))`,
		"\xff\xfe",
		`. == 100000000000000000001`,
		`.a | test("^\\d+$")`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > maxFuzzInput {
			t.Skip("input over the fuzz size bound")
		}
		plain, err := Format(src, false)
		if err != nil {
			return // gojq rejects the source; the printer never sees it.
		}
		again, err := Format(plain, false)
		require.NoErrorf(t, err, "formatted output must re-parse\n src:  %q\n out:  %q", src, plain)
		require.Equalf(t, plain, again, "Format is not idempotent\n src: %q", src)

		colored, err := Format(src, true)
		require.NoErrorf(t, err, "colored format must accept what plain format accepted\n src: %q", src)
		require.Equalf(t, plain, sgrEscape.ReplaceAllString(colored, ""),
			"colored output differs from plain beyond the SGR escapes\n src: %q", src)
	})
}

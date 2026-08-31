package render_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/render"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func TestJSONPlainIsByteIdentical(t *testing.T) {
	t.Parallel()
	// The plain encoder never HTML-escapes and matches the two-space stdlib form.
	got, err := render.JSON(map[string]any{"a": "<b>", "n": 1}, false)
	require.NoError(t, err)
	// Line by line, not JSONEq: the literal pins both the two-space layout and
	// the unescaped `<`, either of which a semantic comparison would forgive.
	require.Equal(t, []string{"{", `  "a": "<b>",`, `  "n": 1`, "}"}, strings.Split(got, "\n"))
	require.NotContains(t, got, "\x1b[", "plain output has no escapes")
}

func TestJSONColoredSortsAndColors(t *testing.T) {
	t.Parallel()
	got, err := render.JSON(map[string]any{"b": 2, "a": 1}, true)
	require.NoError(t, err)
	require.Contains(t, got, "\x1b[", "colored output has ANSI escapes")
	// Map keys are sorted, so the colored order matches the stdlib encoder's.
	plain := stripANSI(got)
	require.Less(t, strings.Index(plain, `"a"`), strings.Index(plain, `"b"`))
}

func TestJSONColoredIndentsWhenPretty(t *testing.T) {
	t.Parallel()
	// The colored pretty form is multi-line with two-space indentation; stripping
	// the ANSI leaves the same layout as the plain encoder.
	got, err := render.JSON(map[string]any{"a": 1}, true)
	require.NoError(t, err)
	require.Contains(t, stripANSI(got), "\n  \"a\"")
}

func TestNewJSONEncoderColoredCompactHasNoIndent(t *testing.T) {
	t.Parallel()
	// Compact colored output stays on one line: no newline-plus-indent appears.
	var buf bytes.Buffer
	require.NoError(t, render.NewJSONEncoder(&buf, "", "", true).Encode(map[string]any{"a": 1, "b": 2}))
	require.NotContains(t, stripANSI(buf.String()), "\n  ")
	require.Contains(t, buf.String(), "\x1b[")
}

func TestNewJSONEncoderStreamsPlain(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	enc := render.NewJSONEncoder(&buf, "", "", false) // compact
	require.NoError(t, enc.Encode(map[string]any{"n": 1}))
	require.Equal(t, "{\"n\":1}\n", buf.String())
}

func TestJSONColoredNeverHTMLEscapes(t *testing.T) {
	t.Parallel()
	// The sink is a terminal, not a web page, so the colored encoder leaves the
	// HTML metacharacters alone exactly as the plain one does.
	got, err := render.JSON(map[string]any{"a": "<b>&c"}, true)
	require.NoError(t, err)
	require.Equal(t, []string{"{", `  "a": "<b>&c"`, "}"}, strings.Split(stripANSI(got), "\n"))
	require.NotContains(t, got, `\u003c`, "colored output is not HTML-escaped")
}

func TestNewJSONEncoderColoredSortsMapKeys(t *testing.T) {
	t.Parallel()
	// Go randomises map iteration, so one pass over a sorted-looking result
	// proves nothing; eight keys re-encoded ten times cannot land in order by
	// chance if the encoder is not sorting.
	v := map[string]any{"h": 8, "g": 7, "f": 6, "e": 5, "d": 4, "c": 3, "b": 2, "a": 1}
	const want = `{"a":1,"b":2,"c":3,"d":4,"e":5,"f":6,"g":7,"h":8}` + "\n"
	for i := range 10 {
		var buf bytes.Buffer

		require.NoError(t, render.NewJSONEncoder(&buf, "", "", true).Encode(v))

		require.Equal(t, want, stripANSI(buf.String()), "pass %d", i)
	}
}

func TestJSONReportsAnEncodeFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		colored bool
	}{
		{"plain", false},
		{"colored", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// A channel has no JSON form, so the encoder fails and JSON must
			// surface that rather than return the half-written buffer.
			got, err := render.JSON(make(chan int), tt.colored)

			require.ErrorContains(t, err, "encode json")
			// Wrapped, not flattened: the encoder's own cause stays reachable.
			var cause *json.UnsupportedTypeError
			require.ErrorAs(t, err, &cause)
			require.Equal(t, reflect.TypeFor[chan int](), cause.Type)
			require.Empty(t, got)
		})
	}
}

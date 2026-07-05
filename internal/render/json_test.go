package render_test

import (
	"bytes"
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
	require.Equal(t, "{\n  \"a\": \"<b>\",\n  \"n\": 1\n}", got)
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

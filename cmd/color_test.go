package cmd

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/diff"
	"github.com/zsltg/iq/internal/render"
)

// TestValuesFormatterColored proves the --raw rendering colors its composite
// fallback and non-string scalars but keeps bare strings and nulls uncolored,
// and that with color on the visible bytes still equal the plain rendering.
func TestValuesFormatterColored(t *testing.T) {
	// Not parallel: flips the global color mode.
	orig := color.NoColor
	t.Cleanup(func() { color.NoColor = orig })

	tests := []struct {
		name    string
		val     any
		colored bool // whether the colored rendering should carry ANSI escapes
	}{
		{name: "number", val: 42, colored: true},
		{name: "bool", val: true, colored: true},
		{name: "object", val: map[string]any{"name": "alice", "year": 2020}, colored: true},
		{name: "string is bare and uncolored", val: "alice", colored: false},
		{name: "nil is bare and uncolored", val: nil, colored: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			color.NoColor = true
			plain := renderFormatter(t, formatValues, false, tt.val)
			color.NoColor = false
			got := renderFormatter(t, formatValues, false, tt.val)
			require.Equal(t, plain, stripANSI(got), "stripANSI must equal the plain rendering")
			if tt.colored {
				require.Contains(t, got, "\x1b[", "colored rendering must carry ANSI escapes")
			} else {
				require.NotContains(t, got, "\x1b[", "bare text must carry no escapes")
			}
		})
	}
}

// TestGronFormatterColored proves both gron variants strip back to their plain,
// ungron-safe output byte-for-byte and that a *big.Int still renders bare under
// color (it implements json.Marshaler; the colored encoder must not break that).
func TestGronFormatterColored(t *testing.T) {
	// Not parallel: flips the global color mode.
	orig := color.NoColor
	t.Cleanup(func() { color.NoColor = orig })

	tests := []struct {
		name string
		fm   outputFormat
		vals []any
	}{
		{name: "gron scalars and nesting", fm: formatGron, vals: []any{map[string]any{"a": 1, "b": []any{"x", 2}}}},
		{name: "grona indexes with a leading declaration", fm: formatGronArray, vals: []any{map[string]any{"a": 1}, "s"}},
		{name: "gron big integer renders bare", fm: formatGron, vals: []any{mustBigInt("12345678901234567890")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			color.NoColor = true
			plain := renderFormatter(t, tt.fm, false, tt.vals...)
			color.NoColor = false
			got := renderFormatter(t, tt.fm, false, tt.vals...)
			require.Equal(t, plain, stripANSI(got), "colored gron must strip back to the plain output")
			require.Contains(t, got, "\x1b[", "colored gron must carry ANSI escapes")
		})
	}
}

// TestGronColoredSegmentRoles pins the exact palette roles: the json root and a
// bare key segment in the Key role, an array index in the Number role (brackets
// plain), a string rhs in the String role, and grona's leading declaration.
func TestGronColoredSegmentRoles(t *testing.T) {
	// Not parallel: flips the global color mode.
	orig := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = orig })

	const reset = "\x1b[0m"
	got := renderFormatter(t, formatGron, false, map[string]any{"name": "alice", "arr": []any{1}})
	require.Contains(t, got, render.ColorKey+"json"+reset, "json root in the Key role")
	require.Contains(t, got, render.ColorKey+".name"+reset, "bare key segment in the Key role")
	require.Contains(t, got, "["+render.ColorNumber+"0"+reset+"]", "array index in the Number role, brackets plain")
	require.Contains(t, got, render.ColorString+`"alice"`, "string rhs in the String role")

	gotA := renderFormatter(t, formatGronArray, false, "s")
	require.Contains(t, gotA, render.ColorKey+"json"+reset+" = [];", "grona leading declaration keeps json in the Key role")
}

// ansiRE matches SGR escape sequences, so a colored rendering can be reduced to
// its visible text.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func TestWantColor(t *testing.T) {
	// Not parallel: subtests set NO_COLOR via t.Setenv.
	buf := &bytes.Buffer{} // not an *os.File, so never a terminal
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })

	tests := []struct {
		name       string
		monochrome bool
		force      bool
		noColor    bool
		w          io.Writer
		want       bool
	}{
		{name: "monochrome forces off", monochrome: true, w: buf},
		{name: "monochrome beats force", monochrome: true, force: true, w: buf},
		{name: "force beats non-terminal", force: true, w: buf, want: true},
		{name: "force beats NO_COLOR", force: true, noColor: true, w: buf, want: true},
		{name: "NO_COLOR disables", noColor: true, w: pw},
		{name: "non-file writer is not a tty", w: buf},
		{name: "pipe is not a tty", w: pw},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.noColor {
				t.Setenv("NO_COLOR", "1")
			} else {
				// A parent process may export NO_COLOR; clear it for the auto cases.
				t.Setenv("NO_COLOR", "")
				require.NoError(t, os.Unsetenv("NO_COLOR"))
			}
			got := wantColor(tt.monochrome, tt.force, tt.w)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestColorizeYAML(t *testing.T) {
	t.Parallel()
	out := colorizeYAML("a: 1\nb: x\n")
	require.Contains(t, out, "\x1b[", "colored YAML must contain ANSI escapes")
	require.True(t, strings.HasSuffix(out, "\n"), "trailing newline must be restored")
	require.Contains(t, stripANSI(out), "a: 1")
	require.Contains(t, stripANSI(out), "b: x")
}

func TestColorYAMLFormatterStreamsDocuments(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	f := &colorYAMLFormatter{w: &buf}
	require.NoError(t, f.emit("first"))
	require.NoError(t, f.emit("second"))
	require.NoError(t, f.flush())
	plain := stripANSI(buf.String())
	// The second and later documents carry the `---` separator; the first does not.
	require.Equal(t, "first\n---\nsecond\n", plain)
}

func TestOpColor(t *testing.T) {
	t.Parallel()
	require.Equal(t, pal.add, opColor(diff.OpAdd))
	require.Equal(t, pal.remove, opColor(diff.OpRemove))
	require.Equal(t, pal.change, opColor(diff.OpChange))
	require.Nil(t, opColor(diff.Op(99)))
}

func TestColorLine(t *testing.T) {
	// Not parallel: flips the global color mode.
	orig := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = orig })

	// A colored op wraps the whole line; an op with no color returns it plain.
	require.Equal(t, pal.add.Sprint("+ k  1"), colorLine(diff.OpAdd, "+ k  1"))
	require.Contains(t, colorLine(diff.OpRemove, "- k  1"), "\x1b[")
	require.Equal(t, "? k  1", colorLine(diff.Op(99), "? k  1"))
}

func TestRenderPlainTableMatchesTabwriter(t *testing.T) {
	t.Parallel()
	rows := [][]tableCell{
		{cell("alpha"), cell("redis"), cell("ok"), cell("2ms")},
		{cell("longername"), cell("mongodb"), cell("error"), cell("boom")},
	}
	var buf bytes.Buffer
	require.NoError(t, renderPlainTable(&buf, rows))
	want := "alpha       redis    ok     2ms\n" +
		"longername  mongodb  error  boom\n"
	require.Equal(t, want, buf.String())
}

// TestColorTableAlignment proves the manual colored renderer reproduces the
// tabwriter layout exactly once the ANSI escapes are stripped, so color never
// distorts column alignment.
func TestColorTableAlignment(t *testing.T) {
	// Not parallel: flips the global color mode.
	orig := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = orig })

	rows := [][]tableCell{
		{coloredCell("* prod", pal.active), cell("redis://host"), cell("(books)")},
		{cell("  dev"), cell("mongodb://host"), cell("")},
	}
	var colored bytes.Buffer
	require.NoError(t, renderTable(&colored, rows))
	require.Contains(t, colored.String(), "\x1b[", "colored table must contain ANSI escapes")

	var plain bytes.Buffer
	require.NoError(t, renderPlainTable(&plain, rows))
	require.Equal(t, plain.String(), stripANSI(colored.String()))
}

func TestRenderTablePlainWhenColorOff(t *testing.T) {
	// Not parallel: asserts behavior under a known global color mode.
	orig := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = orig })

	rows := [][]tableCell{{coloredCell("ok", pal.ok), cell("done")}}
	var buf bytes.Buffer
	require.NoError(t, renderTable(&buf, rows))
	require.NotContains(t, buf.String(), "\x1b[", "no escapes when color is off")
	require.Equal(t, "ok  done\n", buf.String())
}

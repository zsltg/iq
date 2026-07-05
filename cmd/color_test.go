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
)

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

func TestDiffSymbolColors(t *testing.T) {
	// Not parallel: flips the global color mode.
	orig := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = orig })

	// A colored op wraps its symbol; an op with no color returns it plain.
	require.Equal(t, pal.add.Sprint(diff.OpAdd.Symbol()), diffSymbol(diff.OpAdd))
	require.Contains(t, diffSymbol(diff.OpRemove), "\x1b[")
	require.Equal(t, diff.Op(99).Symbol(), diffSymbol(diff.Op(99)))
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

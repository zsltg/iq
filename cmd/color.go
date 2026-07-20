package cmd

import (
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/fatih/color"
	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/printer"
	"github.com/mattn/go-isatty"

	"github.com/zsltg/iq/internal/diff"
)

// palette names the semantic roles the CLI colors. Each *color.Color consults
// the global color.NoColor at render time, so one package-level palette renders
// plain or colored according to the mode resolved for the current invocation.
type palette struct {
	ok       *color.Color
	fail     *color.Color
	add      *color.Color
	remove   *color.Color
	change   *color.Color
	header   *color.Color
	active   *color.Color
	handle   *color.Color
	location *color.Color
	faint    *color.Color
}

// pal is the shared palette. Colors are stateless: they read color.NoColor when
// Sprint runs, not when constructed, so building this at load time is safe even
// though the mode is decided later in the root PersistentPreRunE.
var pal = palette{
	ok:     color.New(color.FgGreen),
	fail:   color.New(color.FgRed),
	add:    color.New(color.FgGreen),
	remove: color.New(color.FgRed),
	change: color.New(color.FgYellow),
	header: color.New(color.FgCyan, color.Bold),
	// active, handle, location, and faint mirror sq's source-list scheme: the
	// active handle green-bold, other handles blue, locations green, and secondary
	// detail (driver, options) faint.
	active:   color.New(color.FgGreen, color.Bold),
	handle:   color.New(color.FgBlue),
	location: color.New(color.FgGreen),
	faint:    color.New(color.Faint),
}

// resolveColor sets the global color mode for the invocation, honoring the
// -M/-C flags (mutually exclusive, rejected in the root PreRun), the NO_COLOR
// convention, and whether w is a terminal. It drives fatih/color's global
// NoColor so every palette color and the colored JSON/YAML encoders follow one
// decision.
func resolveColor(monochrome, force bool, w io.Writer) {
	color.NoColor = !wantColor(monochrome, force, w)
}

// wantColor reports whether to emit ANSI color. -M forces off and wins over
// everything; -C forces on and overrides NO_COLOR and the terminal check;
// otherwise color is on only when NO_COLOR is unset and w is a real terminal.
func wantColor(monochrome, force bool, w io.Writer) bool {
	if monochrome {
		return false
	}
	if force {
		return true
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// colorOn reports whether ANSI color is currently enabled.
func colorOn() bool { return !color.NoColor }

// opColor returns the palette color for a diff operation, or nil for an op with
// no color.
func opColor(op diff.Op) *color.Color {
	switch op {
	case diff.OpAdd:
		return pal.add
	case diff.OpRemove:
		return pal.remove
	case diff.OpChange:
		return pal.change
	default:
		return nil
	}
}

// colorLine wraps a whole rendered line in its operation's color, or returns it
// plain for an op with no color. One color wrap over the line keeps a single
// escape pair around add/remove rows rather than stitching per-fragment colors.
func colorLine(op diff.Op, s string) string {
	if c := opColor(op); c != nil {
		return c.Sprint(s)
	}
	return s
}

// yamlColorPrinter colorizes YAML tokens with roles matching the JSON palette:
// keys cyan-bold, strings green, numbers magenta, booleans yellow.
var yamlColorPrinter = printer.Printer{
	MapKey: yamlProp("1;36"),
	String: yamlProp("32"),
	Number: yamlProp("35"),
	Bool:   yamlProp("33"),
}

// yamlProp builds a goccy print function wrapping a token in the given SGR code.
func yamlProp(code string) printer.PrintFunc {
	return func() *printer.Property {
		return &printer.Property{Prefix: "\x1b[" + code + "m", Suffix: "\x1b[0m"}
	}
}

// colorizeYAML returns doc with its tokens colored. goccy's printer drops the
// trailing newline, so it is restored to match the uncolored encoder's output.
func colorizeYAML(doc string) string {
	out := yamlColorPrinter.PrintTokens(lexer.Tokenize(doc))
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

// tableCell is one column of a row: its text and the color to render it in (nil
// for none). The color is applied only in the colored renderer; the plain
// tabwriter path ignores it.
type tableCell struct {
	text string
	c    *color.Color
}

// cell builds an uncolored table cell.
func cell(text string) tableCell { return tableCell{text: text} }

// coloredCell builds a table cell rendered in c.
func coloredCell(text string, c *color.Color) tableCell { return tableCell{text: text, c: c} }

// renderTable writes an aligned table. With color off it uses text/tabwriter,
// preserving the exact uncolored layout. With color on it pads columns by their
// visible (uncolored) width and then colors each cell, so ANSI escapes never
// distort the alignment the way they would inside tabwriter.
func renderTable(out io.Writer, rows [][]tableCell) error {
	if !colorOn() {
		return renderPlainTable(out, rows)
	}
	widths := columnWidths(rows)
	var b strings.Builder
	for _, row := range rows {
		for j, c := range row {
			text := c.text
			if c.c != nil {
				text = c.c.Sprint(c.text)
			}
			b.WriteString(text)
			// A column has a width only if it is non-terminal; the trailing cell
			// of each row is never padded. widths is the single source of that
			// distinction, so the pad guard reads its length, not len(row).
			if j < len(widths) {
				b.WriteString(strings.Repeat(" ", widths[j]-utf8.RuneCountInString(c.text)+2))
			}
		}
		b.WriteByte('\n')
	}
	_, err := io.WriteString(out, b.String())
	return err
}

// renderPlainTable writes rows through a text/tabwriter configured exactly as the
// commands did before color: no minwidth, padding 2, space padding.
func renderPlainTable(out io.Writer, rows [][]tableCell) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, row := range rows {
		texts := make([]string, len(row))
		for j, c := range row {
			texts[j] = c.text
		}
		if _, err := io.WriteString(w, strings.Join(texts, "\t")+"\n"); err != nil {
			return err
		}
	}
	return w.Flush()
}

// columnWidths returns the max visible width of each non-terminal column. The
// final cell of a row is trailing (never padded), so it does not count toward any
// column width, matching text/tabwriter.
func columnWidths(rows [][]tableCell) []int {
	var w []int
	for _, row := range rows {
		for j := 0; j < len(row)-1; j++ {
			if j >= len(w) {
				w = append(w, 0)
			}
			w[j] = max(w[j], utf8.RuneCountInString(row[j].text))
		}
	}
	return w
}

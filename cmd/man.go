package cmd

import (
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// newManCmd builds `iq man`: print the iq(1) manual page in roff to stdout, for
// `iq man | sudo tee /usr/share/man/man1/iq.1`. The page is a deterministic
// function of the command tree — no date or version — so its committed copy
// (docs/man/iq.1) is byte-stable and a drift-guard test can catch staleness.
func newManCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "man",
		Short:             "Print the iq(1) manual page (roff)",
		Example:           "  $ iq man | sudo tee /usr/share/man/man1/iq.1",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return writeManPage(cmd.OutOrStdout(), cmd.Root())
		},
	}
}

// writeManPage renders the iq(1) manual page in roff to w, derived from the Cobra
// command tree so it can never drift from --help. It embeds no version or date:
// the page is a static function of the command definitions, which keeps
// regeneration deterministic and its golden test meaningful. The build version is
// a runtime fact, available via `iq version`.
//
// To stay independent of Cobra's execute-time initialization (which adds the
// auto help/completion commands and per-command --help flags), the page walks
// only the grouped commands — every real iq command — and omits the auto-added
// help flag, so the CLI output and the direct-call golden test are byte-identical.
func writeManPage(w io.Writer, root *cobra.Command) error {
	var b strings.Builder

	// man(7) header: name, section, empty date/version (determinism), source and
	// manual title.
	b.WriteString(".TH IQ 1 \"\" \"iq\" \"User Commands\"\n")

	b.WriteString(".SH NAME\n")
	b.WriteString(manEsc(root.Name()) + " \\- " + manLine(root.Short) + "\n")

	b.WriteString(".SH SYNOPSIS\n")
	b.WriteString(".B iq\n")
	b.WriteString("[\\fIjq\\-filter\\fR] [\\fIflags\\fR]\n")
	b.WriteString(".br\n")
	b.WriteString(".B iq\n")
	b.WriteString("\\fIcommand\\fR [\\fIargs\\fR]\n")

	b.WriteString(".SH DESCRIPTION\n")
	writeManProse(&b, root.Long)

	b.WriteString(".SH GLOBAL FLAGS\n")
	writeManGlobalFlags(&b, root)

	b.WriteString(".SH COMMANDS\n")
	writeManCommands(&b, root)

	if root.Example != "" {
		b.WriteString(".SH EXAMPLES\n")
		writeManExampleBlock(&b, root.Example)
	}

	b.WriteString(".SH SEE ALSO\n")
	b.WriteString(".PP\n")
	b.WriteString("jq(1).\n")
	b.WriteString(".PP\n")
	b.WriteString(manLine("The iq README and the documentation site document every command, "+
		"driver, and option in full.") + "\n")

	b.WriteString(".SH AUTHORS\n")
	b.WriteString("iq is written by Zsolt Gaspar.\n")

	_, err := io.WriteString(w, b.String())
	return err
}

// writeManGlobalFlags renders the root's persistent and local flags as .TP
// entries, grouped and ordered exactly as the --help screen (flagGroupOrder /
// rootFlagGroups), so the page mirrors the help output. Auto-added flags
// (--help/--version) carry no group annotation and are omitted.
func writeManGlobalFlags(b *strings.Builder, root *cobra.Command) {
	for _, group := range flagGroupOrder {
		names := make([]string, 0, len(rootFlagGroups))
		for name, g := range rootFlagGroups {
			if g == group {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			f := rootFlag(root, name)
			if f == nil || f.Hidden {
				continue
			}
			writeManFlag(b, f)
		}
	}
}

// rootFlag resolves a root flag by name from whichever set holds it (persistent
// flags are declared in PersistentFlags, local ones in Flags).
func rootFlag(root *cobra.Command, name string) *pflag.Flag {
	if f := root.PersistentFlags().Lookup(name); f != nil {
		return f
	}
	return root.Flags().Lookup(name)
}

// writeManCommands renders every command as a .SS subsection, walking the root's
// grouped subcommands in help-group order and recursing into their nested
// subcommands. Only grouped commands are walked, so the set is exactly the real
// iq commands and does not depend on Cobra's execute-time initialization.
func writeManCommands(b *strings.Builder, root *cobra.Command) {
	for _, g := range rootCommandGroupOrder {
		for _, sub := range root.Commands() {
			if sub.Hidden || sub.GroupID != g.ID {
				continue
			}
			writeManCommandTree(b, sub)
		}
	}
	// Cobra adds the completion command at Execute time, so a fresh root does not
	// carry it yet; initialize it explicitly so the page is identical whether it
	// is generated through `iq man` or straight from newRootCmd. It carries no
	// group, so the grouped walk above misses it, and the README and docs tell
	// users to run it, so the page documents it after the grouped commands.
	root.InitDefaultCompletionCmd()
	for _, sub := range root.Commands() {
		if !sub.Hidden && sub.Name() == "completion" {
			writeManCommandTree(b, sub)
		}
	}
}

// writeManCommandTree renders one command as a .SS subsection — full path, one-
// line summary, usage line, long description, local flags, and examples — then
// recurses into its visible subcommands (Cobra returns them name-sorted, so the
// order is deterministic).
func writeManCommandTree(b *strings.Builder, cmd *cobra.Command) {
	b.WriteString(".SS " + manEsc(cmd.CommandPath()) + "\n")
	b.WriteString(manLine(cmd.Short) + "\n")
	b.WriteString(".PP\n")
	b.WriteString("\\fBUsage:\\fR " + manEsc(manUsageLine(cmd)) + "\n")

	if cmd.Long != "" {
		writeManProse(b, cmd.Long)
	}
	writeManLocalFlags(b, cmd)
	if cmd.Example != "" {
		b.WriteString(".PP\n\\fBExamples:\\fR\n")
		writeManExampleBlock(b, cmd.Example)
	}

	for _, sub := range cmd.Commands() {
		if sub.Hidden {
			continue
		}
		writeManCommandTree(b, sub)
	}
}

// manUsageLine builds a command's usage line as its full path plus the argument
// spec from its Use field (the tokens after the command name), deterministically:
// unlike cobra's UseLine it never appends a " [flags]" suffix that depends on
// execute-time help-flag initialization.
func manUsageLine(cmd *cobra.Command) string {
	if i := strings.IndexByte(cmd.Use, ' '); i >= 0 {
		return cmd.CommandPath() + cmd.Use[i:]
	}
	return cmd.CommandPath()
}

// writeManLocalFlags renders a command's own (non-inherited) flags as a .TP list,
// lexical by name (pflag's default), skipping hidden flags and the auto-added
// --help so the output is independent of Cobra's execute-time initialization. A
// command with no own flags renders nothing.
func writeManLocalFlags(b *strings.Builder, cmd *cobra.Command) {
	var flags []*pflag.Flag
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		flags = append(flags, f)
	})
	if len(flags) == 0 {
		return
	}
	b.WriteString(".PP\n\\fBFlags:\\fR\n")
	for _, f := range flags {
		writeManFlag(b, f)
	}
}

// writeManFlag renders one flag as a roff tagged paragraph (.TP): its name(s) in
// bold, then the usage text with the default appended when it is meaningful.
func writeManFlag(b *strings.Builder, f *pflag.Flag) {
	b.WriteString(".TP\n")
	b.WriteString(manFlagNames(f) + "\n")
	usage := f.Usage
	if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
		usage += " (default " + f.DefValue + ")"
	}
	b.WriteString(manLine(usage) + "\n")
}

// manFlagNames renders a flag's names in bold: "-s, --src" when it has a
// shorthand, else "--src".
func manFlagNames(f *pflag.Flag) string {
	long := "\\fB" + manEsc("--"+f.Name) + "\\fR"
	if f.Shorthand != "" {
		return "\\fB" + manEsc("-"+f.Shorthand) + "\\fR, " + long
	}
	return long
}

// writeManProse renders a description block: each blank-line-separated paragraph
// as a filled .PP paragraph, except an indented block (an inline example, whose
// first line begins with whitespace) which is rendered verbatim in a no-fill
// region so its layout survives.
func writeManProse(b *strings.Builder, text string) {
	for para := range strings.SplitSeq(text, "\n\n") {
		if para == "" {
			continue
		}
		if isIndentedBlock(para) {
			b.WriteString(".PP\n.RS\n.nf\n")
			for ln := range strings.SplitSeq(para, "\n") {
				b.WriteString(manVerbatim(ln) + "\n")
			}
			b.WriteString(".fi\n.RE\n")
			continue
		}
		b.WriteString(".PP\n")
		b.WriteString(manLine(para) + "\n")
	}
}

// isIndentedBlock reports whether a paragraph's first line begins with a space or
// tab, marking it as a preformatted (example) block rather than fillable prose.
func isIndentedBlock(para string) bool {
	return strings.HasPrefix(para, " ") || strings.HasPrefix(para, "\t")
}

// writeManExampleBlock renders a multi-line example string verbatim in a no-fill
// indented region, so wrapping and spacing survive. A blank line stays blank.
func writeManExampleBlock(b *strings.Builder, example string) {
	b.WriteString(".RS\n.nf\n")
	for ln := range strings.SplitSeq(example, "\n") {
		if ln == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(manVerbatim(ln) + "\n")
	}
	b.WriteString(".fi\n.RE\n")
}

// manLine collapses a paragraph's internal newlines to spaces (roff fills anyway)
// and escapes it for a fill-mode line.
func manLine(para string) string {
	return manVerbatim(strings.ReplaceAll(para, "\n", " "))
}

// manPunct maps the non-ASCII punctuation that appears in the command prose to its
// roff escape, so the page stays 7-bit ASCII and renders identically under any
// locale (a raw UTF-8 em-dash is an "invalid input character" to -Tascii troff).
var manPunct = strings.NewReplacer(
	"—", "\\(em", // em dash
	"–", "\\(en", // en dash
	"‘", "\\(oq", // left single quote
	"’", "\\(cq", // right single quote / apostrophe
	"“", "\\(lq", // left double quote
	"”", "\\(rq", // right double quote
	"…", "...", // horizontal ellipsis
)

// manVerbatim escapes one roff output line: backslashes become the escape-glyph
// sequence, non-ASCII punctuation becomes its roff escape, hyphens become literal
// minus signs (so copy-paste of flags works and roff does not hyphenate them),
// and a leading control character is neutralized with the zero-width \& so the
// line is never read as a request. Order matters: existing backslashes are
// doubled first, before the escapes we introduce.
func manVerbatim(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\e")
	s = manPunct.Replace(s)
	s = strings.ReplaceAll(s, "-", "\\-")
	return guardControl(s)
}

// manEsc is manVerbatim for inline fragments (names, usage strings) that never
// span multiple source lines.
func manEsc(s string) string {
	return manVerbatim(s)
}

// guardControl prefixes the zero-width \& when a line would otherwise begin with
// a roff control character ('.' or a single quote), which would make roff treat
// the line as a request rather than text.
func guardControl(s string) string {
	if strings.HasPrefix(s, ".") || strings.HasPrefix(s, "'") {
		return "\\&" + s
	}
	return s
}

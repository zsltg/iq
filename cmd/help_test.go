package cmd

import (
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

// TestRootHelpGroupsFlags pins the grouped --help rendering: every section
// header appears, in the fixed order, and each header precedes its member flags.
func TestRootHelpGroupsFlags(t *testing.T) {
	root, _ := newRootCmd()

	out, err := runCmd(t, root, "--help")
	require.NoError(t, err)

	// Section headers appear in flagGroupOrder, all after the "Flags:" block
	// opener.
	wantOrder := []string{
		"Flags:",
		"  Source:",
		"  Query:",
		"  Output:",
		"  Display:",
		"  Diagnostics:",
		"  Options:",
	}
	prev := -1
	for _, h := range wantOrder {
		at := strings.Index(out, h)
		require.NotEqualf(t, -1, at, "header %q missing from help", h)
		require.Greaterf(t, at, prev, "header %q out of order", h)
		prev = at
	}

	// A flag lands under its own section, not merely somewhere in the output.
	cases := []struct{ header, flag string }{
		{"  Source:", "--src"},
		{"  Query:", "--no-compile"},
		{"  Output:", "--jsona"},
		{"  Display:", "--no-progress"},
		{"  Diagnostics:", "--debug.pprof"},
	}
	for _, c := range cases {
		t.Run(c.flag, func(t *testing.T) {
			section := sectionAfter(out, c.header)
			require.Containsf(t, section, c.flag, "%s not under %s", c.flag, c.header)
		})
	}
}

// TestRootHelpEveryFlagGrouped is the coverage guard: every root flag (bar the
// auto-added help/version) carries a group annotation naming a real section, so
// a future flag added without a group fails here instead of silently landing in
// "Options".
func TestRootHelpEveryFlagGrouped(t *testing.T) {
	root, _ := newRootCmd()

	realGroups := map[string]bool{
		groupSource: true, groupQuery: true, groupOutput: true, groupWrite: true,
		groupDisplay: true, groupDiagnostics: true,
	}
	check := func(f *pflag.Flag) {
		if f.Name == "help" || f.Name == "version" {
			return
		}
		vals := f.Annotations[flagGroupKey]
		require.Lenf(t, vals, 1, "flag --%s has no group annotation", f.Name)
		require.Truef(t, realGroups[vals[0]], "flag --%s in unknown group %q", f.Name, vals[0])
	}
	root.PersistentFlags().VisitAll(check)
	root.Flags().VisitAll(check)

	// The orphan direction: every mapped name must resolve to a registered root
	// flag, so a retired flag's entry cannot linger in the map unnoticed.
	for name := range rootFlagGroups {
		if root.PersistentFlags().Lookup(name) == nil && root.Flags().Lookup(name) == nil {
			t.Errorf("rootFlagGroups maps %q, which is not a registered root flag", name)
		}
	}
}

// TestSubcommandHelpGroupsGlobalFlags pins that a subcommand groups its inherited
// (global) flags while its own local flags stay a flat list.
func TestSubcommandHelpGroupsGlobalFlags(t *testing.T) {
	root, _ := newRootCmd()

	out, err := runCmd(t, root, "diff", "--help")
	require.NoError(t, err)

	global := out[strings.Index(out, "Global Flags:"):]
	require.Contains(t, global, "  Source:")
	require.Contains(t, global, "  Diagnostics:")
	require.Contains(t, global, "--timeout")

	// diff's own flags render under the flat "Flags:" block, above Global Flags.
	local := out[strings.Index(out, "Flags:"):strings.Index(out, "Global Flags:")]
	require.Contains(t, local, "--data")
	require.NotContains(t, local, "  Source:")
}

// TestGroupedFlagUsages exercises the template func directly: an annotated set
// renders sections in order; an unannotated set is byte-identical to pflag's
// default so subcommand-local flags are untouched.
func TestGroupedFlagUsages(t *testing.T) {
	t.Run("annotated set is grouped", func(t *testing.T) {
		fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
		fs.String("src", "", "source")
		fs.Bool("json", false, "json out")
		fs.Bool("loose", false, "no group") // unannotated -> Options
		require.NoError(t, fs.SetAnnotation("src", flagGroupKey, []string{groupSource}))
		require.NoError(t, fs.SetAnnotation("json", flagGroupKey, []string{groupOutput}))

		got := groupedFlagUsages(fs)

		srcAt := strings.Index(got, "  Source:")
		outAt := strings.Index(got, "  Output:")
		optAt := strings.Index(got, "  Options:")
		require.NotEqual(t, -1, srcAt)
		require.Less(t, srcAt, outAt)
		require.Less(t, outAt, optAt)
		require.Contains(t, sectionAfter(got, "  Source:"), "--src")
		require.Contains(t, sectionAfter(got, "  Options:"), "--loose")

		// The first section starts flush (no leading blank line) and consecutive
		// sections are separated by exactly one blank line — pins the b.Len() > 0
		// separator guard.
		require.True(t, strings.HasPrefix(got, "  Source:"), "unexpected leading blank line: %q", got)
		require.Contains(t, got, "\n\n  Output:")
		require.Contains(t, got, "\n\n  Options:")
	})

	t.Run("unannotated set is unchanged", func(t *testing.T) {
		fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
		fs.Bool("data", false, "diff data")
		fs.Int("sample", 1000, "sample size")

		require.Equal(t, fs.FlagUsages(), groupedFlagUsages(fs))
	})
}

// sectionAfter returns the slice of s starting just past header and ending at the
// next two-space-indented section header (or end of string), i.e. the flag lines
// that belong to that section.
func sectionAfter(s, header string) string {
	start := strings.Index(s, header)
	if start == -1 {
		return ""
	}
	start += len(header)
	rest := s[start:]
	// The next section starts at a blank line followed by "  <Title>:"; scan
	// line by line for the next header at the same indent.
	lines := strings.Split(rest, "\n")
	var b strings.Builder
	for i, line := range lines {
		if i > 0 && isSectionHeader(line) {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// isSectionHeader reports whether line is a "  <Title>:" group header (two-space
// indent, single word, trailing colon), distinguishing it from a "    --flag"
// entry indented four spaces.
func isSectionHeader(line string) bool {
	if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") {
		return false
	}
	trimmed := strings.TrimSpace(line)
	return strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed, " ")
}

// TestRootHelpGroupsCommands pins the grouped "Available Commands" rendering:
// every group title appears, in the fixed order, and each title precedes its
// member commands. Cobra renders group titles unindented (unlike the "  <Title>:"
// flag section headers above), so this uses sectionBetween instead of sectionAfter.
func TestRootHelpGroupsCommands(t *testing.T) {
	root, _ := newRootCmd()

	out, err := runCmd(t, root, "--help")
	require.NoError(t, err)

	wantOrder := []string{
		"Sources:",
		"Query & Data:",
		"Configuration:",
		"Info:",
		"Additional Commands:",
	}
	prev := -1
	for _, h := range wantOrder {
		at := strings.Index(out, h)
		require.NotEqualf(t, -1, at, "header %q missing from help", h)
		require.Greaterf(t, at, prev, "header %q out of order", h)
		prev = at
	}

	cases := []struct{ header, next, cmd string }{
		{"Sources:", "Query & Data:", "add"},
		{"Sources:", "Query & Data:", "ping"},
		{"Query & Data:", "Configuration:", "exec"},
		{"Query & Data:", "Configuration:", "diff"},
		{"Configuration:", "Info:", "config"},
		{"Info:", "Additional Commands:", "driver"},
		{"Info:", "Additional Commands:", "version"},
	}
	for _, c := range cases {
		t.Run(c.cmd, func(t *testing.T) {
			section := sectionBetween(out, c.header, c.next)
			require.Containsf(t, section, c.cmd, "%s not under %s", c.cmd, c.header)
		})
	}

	// help/completion are Cobra's own, deliberately ungrouped.
	additional := out[strings.Index(out, "Additional Commands:"):]
	require.Contains(t, additional, "completion")
	require.Contains(t, additional, "help")
}

// TestRootHelpEveryCommandGrouped is the coverage guard: every root subcommand
// (bar Cobra's auto-added help/completion) carries a GroupID naming a real
// group, so a future command added to root.AddCommand without a group fails
// here instead of silently landing in "Additional Commands:".
func TestRootHelpEveryCommandGrouped(t *testing.T) {
	root, _ := newRootCmd()

	realGroups := map[string]bool{
		cmdGroupSources: true, cmdGroupQuery: true, cmdGroupConfig: true, cmdGroupInfo: true,
	}
	for _, sub := range root.Commands() {
		if sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		require.NotEmptyf(t, sub.GroupID, "command %q has no group", sub.Name())
		require.Truef(t, realGroups[sub.GroupID], "command %q in unknown group %q", sub.Name(), sub.GroupID)
	}
}

// TestExamplesUseLiveFlags is the drift guard for --help: every flag spelled in
// an Example block resolves against the command that example actually invokes,
// so help text that still advertises a retired flag fails here. The root example
// outlived the --from/--combine pair that `iq combine` replaced, and shipped
// teaching a spelling the binary rejects.
func TestExamplesUseLiveFlags(t *testing.T) {
	root, _ := newRootCmd()

	for _, c := range allCommands(root) {
		if c.Example == "" {
			continue
		}
		t.Run(c.CommandPath(), func(t *testing.T) {
			for _, line := range exampleInvocations(c.Example) {
				target, _, err := root.Find(line)
				require.NoErrorf(t, err, "example %q names no command", strings.Join(line, " "))
				for _, tok := range line {
					name, ok := flagName(tok)
					if !ok {
						continue
					}
					require.NotNilf(t, lookupFlag(target, name),
						"example on %q spells %s, which %q does not define",
						c.CommandPath(), tok, target.CommandPath())
				}
			}
		})
	}
}

// allCommands returns cmd and every command beneath it, depth-first.
func allCommands(cmd *cobra.Command) []*cobra.Command {
	out := []*cobra.Command{cmd}
	for _, sub := range cmd.Commands() {
		out = append(out, allCommands(sub)...)
	}
	return out
}

// exampleInvocations extracts the `iq ...` invocations from an Example block as
// token slices, with the leading "iq" dropped so each slice is what Find takes.
// A trailing "\" continues an invocation onto the next line; single-quoted spans
// (jq programs, URLs) are dropped whole, since a flag is never spelled inside
// one and their contents would otherwise tokenize into noise.
func exampleInvocations(example string) [][]string {
	var out [][]string
	var pending string
	for _, line := range strings.Split(example, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case pending != "":
			// A continuation: already inside an invocation.
		case strings.HasPrefix(line, "$ "):
			line = strings.TrimPrefix(line, "$ ")
		default:
			continue // a comment or a blank separator
		}
		if rest, ok := strings.CutSuffix(line, "\\"); ok {
			pending += rest
			continue
		}
		if toks := invocationTokens(pending + line); toks != nil {
			out = append(out, toks)
		}
		pending = ""
	}
	return out
}

// invocationTokens tokenizes one shell line and returns the arguments to its
// first `iq`, or nil when the line invokes something else entirely. Everything
// from a pipe, redirect or trailing comment onward is not an argument to iq, so
// it is cut.
func invocationTokens(line string) []string {
	line = quotedSpan.ReplaceAllString(line, " arg ")
	toks := strings.Fields(line)
	start := -1
	for i, tok := range toks {
		if tok == "iq" {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return nil
	}
	toks = toks[start:]
	for i, tok := range toks {
		if tok == "|" || tok == ">" || tok == ">>" || tok == "&&" || strings.HasPrefix(tok, "#") {
			return toks[:i]
		}
	}
	return toks
}

// quotedSpan matches a single-quoted span, the only quoting the examples use.
var quotedSpan = regexp.MustCompile(`'[^']*'`)

// flagName reduces one token to the flag name it spells: "--src" and
// "--src=shop" both give "src", and a shorthand gives its single letter. The
// bare "-" (stdin) and a non-flag operand give ok false.
func flagName(tok string) (string, bool) {
	name, ok := strings.CutPrefix(tok, "--")
	if !ok {
		if name, ok = strings.CutPrefix(tok, "-"); !ok || name == "" {
			return "", false
		}
	}
	name, _, _ = strings.Cut(name, "=")
	return name, name != ""
}

// lookupFlag finds a flag on cmd by name or by shorthand, including the ones it
// inherits from root.
func lookupFlag(cmd *cobra.Command, name string) *pflag.Flag {
	for _, set := range []*pflag.FlagSet{cmd.Flags(), cmd.InheritedFlags()} {
		if f := set.Lookup(name); f != nil {
			return f
		}
		if len(name) == 1 {
			if f := set.ShorthandLookup(name); f != nil {
				return f
			}
		}
	}
	return nil
}

// sectionBetween returns the slice of s between the end of from and the start
// of to, i.e. the content of one command group's section.
func sectionBetween(s, from, to string) string {
	start := strings.Index(s, from)
	if start == -1 {
		return ""
	}
	start += len(from)
	end := strings.Index(s[start:], to)
	if end == -1 {
		return s[start:]
	}
	return s[start : start+end]
}

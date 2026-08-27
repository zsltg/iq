package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func TestManVerbatimEscaping(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text unchanged", "hello world", "hello world"},
		{"hyphen becomes literal minus", "--src", "\\-\\-src"},
		{"backslash doubled to escape glyph", `a\b`, `a\eb`},
		{"em dash to roff escape", "a — b", "a \\(em b"},
		{"en dash to roff escape", "a – b", "a \\(en b"},
		{"curly quotes to roff escapes", "‘x’ “y”", "\\(oqx\\(cq \\(lqy\\(rq"},
		{"ellipsis to three dots", "wait…", "wait..."},
		{"leading dot guarded", ".greeting", "\\&.greeting"},
		{"leading quote guarded", "'quoted", "\\&'quoted"},
		{"interior dot not guarded", "a.b", "a.b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, manVerbatim(tt.in))
		})
	}
}

func TestManLineCollapsesNewlines(t *testing.T) {
	require.Equal(t, "one two three", manLine("one\ntwo\nthree"))
}

func TestGuardControl(t *testing.T) {
	require.Equal(t, "\\&.tp", guardControl(".tp"))
	require.Equal(t, "\\&'a", guardControl("'a"))
	require.Equal(t, "safe", guardControl("safe"))
}

func TestManFlagNames(t *testing.T) {
	root, _ := newRootCmd()
	// --src has a shorthand -s.
	require.Equal(t, "\\fB\\-s\\fR, \\fB\\-\\-src\\fR", manFlagNames(rootFlag(root, "src")))
	// --timeout has no shorthand.
	require.Equal(t, "\\fB\\-\\-timeout\\fR", manFlagNames(rootFlag(root, "timeout")))
}

func TestManUsageLine(t *testing.T) {
	root, _ := newRootCmd()
	sub, _, err := root.Find([]string{"config", "set"})
	require.NoError(t, err)
	require.Equal(t, "iq config set [--delete] <option> [<value>]", manUsageLine(sub))

	man, _, err := root.Find([]string{"man"})
	require.NoError(t, err)
	require.Equal(t, "iq man", manUsageLine(man))
}

// TestManPageHasStructure sanity-checks the generated page's skeleton so a
// broken generator fails here with a clear message, not only via the byte-exact
// drift guard.
func TestManPageHasStructure(t *testing.T) {
	root, _ := newRootCmd()
	var b strings.Builder
	require.NoError(t, writeManPage(&b, root))
	out := b.String()

	require.True(t, strings.HasPrefix(out, ".TH IQ 1 \"\" \"iq\" \"User Commands\"\n"))
	for _, section := range []string{
		".SH NAME", ".SH SYNOPSIS", ".SH DESCRIPTION", ".SH GLOBAL FLAGS",
		".SH COMMANDS", ".SH EXAMPLES", ".SH SEE ALSO", ".SH AUTHORS",
	} {
		require.Containsf(t, out, section, "missing section %q", section)
	}
	// A grouped command and a nested subcommand each get a .SS subsection.
	require.Contains(t, out, ".SS iq add")
	require.Contains(t, out, ".SS iq config set")
	require.Contains(t, out, ".SS iq man")
	// The auto-added completion command is documented (the README and docs tell
	// users to run it); cobra's help command stays omitted.
	require.Contains(t, out, ".SS iq completion")
	require.NotContains(t, out, ".SS iq help")
}

// TestManPageDeterministic pins that the page is a pure function of the command
// tree: two renders are byte-identical (no date, version, or map-iteration order
// leaking in).
func TestManPageDeterministic(t *testing.T) {
	var a, b strings.Builder
	r1, _ := newRootCmd()
	require.NoError(t, writeManPage(&a, r1))
	r2, _ := newRootCmd()
	require.NoError(t, writeManPage(&b, r2))
	require.Equal(t, a.String(), b.String())
}

// TestManPageMatchesCommitted is the drift guard: the generated page must equal
// the committed docs/man/iq.1 byte-for-byte, so a help-text edit or a cobra bump
// that changes the page fails until it is regenerated.
func TestManPageMatchesCommitted(t *testing.T) {
	root, _ := newRootCmd()
	var b strings.Builder
	require.NoError(t, writeManPage(&b, root))

	golden, err := os.ReadFile("../docs/man/iq.1")
	require.NoError(t, err, "read committed man page")
	require.Equal(t, string(golden), b.String(),
		"docs/man/iq.1 is stale — run `make man` and commit the result")
}

// TestManCommandWiring pins the `man` command literal: it takes no positional
// args, suppresses file completion, and its RunE prints the roff page. A dropped
// field would make one of these fail.
func TestManCommandWiring(t *testing.T) {
	root, _ := newRootCmd()
	man, _, err := root.Find([]string{"man"})
	require.NoError(t, err)
	require.Equal(t, "man", man.Name())

	// ValidArgsFunction is wired to NoFileCompletions (no path completion for man).
	require.NotNil(t, man.ValidArgsFunction, "man completion wiring dropped")
	_, dir := man.ValidArgsFunction(man, nil, "")
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)

	// Args rejects any positional: `iq man extra` fails rather than running RunE.
	require.NotNil(t, man.Args, "man Args validator dropped")

	t.Run("RunE prints the roff page", func(t *testing.T) {
		r, _ := newRootCmd()
		var out bytes.Buffer
		r.SetOut(&out)
		r.SetErr(&out)
		r.SetArgs([]string{"man"})
		require.NoError(t, r.Execute())
		require.True(t, strings.HasPrefix(out.String(), ".TH IQ 1"),
			"man RunE did not print the roff page")

		var want strings.Builder
		r2, _ := newRootCmd()
		require.NoError(t, writeManPage(&want, r2))
		require.Equal(t, want.String(), out.String())
	})

	t.Run("extra positional is rejected", func(t *testing.T) {
		r, _ := newRootCmd()
		var out bytes.Buffer
		r.SetOut(&out)
		r.SetErr(&out)
		r.SetArgs([]string{"man", "extra"})
		require.Error(t, r.Execute(), "man accepted an unexpected positional")
	})
}

// TestManGlobalFlagsSkipsNilAndHidden covers writeManGlobalFlags' guard: a
// rootFlagGroups-mapped flag that is missing on the root is skipped without a
// nil dereference, and a mapped flag marked Hidden is omitted from the page.
func TestManGlobalFlagsSkipsNilAndHidden(t *testing.T) {
	t.Run("missing mapped flag is skipped, not dereferenced", func(t *testing.T) {
		// A bare root declares none of the mapped flags, so every rootFlag lookup
		// returns nil; the guard must skip them all without panicking.
		root := &cobra.Command{Use: "iq"}
		var b strings.Builder
		require.NotPanics(t, func() { writeManGlobalFlags(&b, root) })
		require.NotContains(t, b.String(), ".TP", "no flags should render")
	})

	t.Run("hidden mapped flag is omitted", func(t *testing.T) {
		root := &cobra.Command{Use: "iq"}
		root.PersistentFlags().String("src", "", "src usage")
		root.PersistentFlags().Duration("timeout", 0, "timeout usage")
		root.PersistentFlags().Lookup("src").Hidden = true
		var b strings.Builder
		writeManGlobalFlags(&b, root)
		out := b.String()
		require.Contains(t, out, "timeout usage", "visible mapped flag must render")
		require.NotContains(t, out, "src usage", "hidden mapped flag must be omitted")
	})
}

// TestRootFlagPrefersPersistent pins that rootFlag returns a persistent-only flag
// from PersistentFlags rather than falling through to the (empty) local set.
func TestRootFlagPrefersPersistent(t *testing.T) {
	root := &cobra.Command{Use: "iq"}
	root.PersistentFlags().String("ponly", "", "persistent only")
	// The flag is not in the local set, so a broken lookup would return nil.
	require.Nil(t, root.Flags().Lookup("ponly"))
	f := rootFlag(root, "ponly")
	require.NotNil(t, f, "rootFlag did not return the persistent flag")
	require.Equal(t, "ponly", f.Name)
}

// TestManCommandsSkipsHiddenGrouped pins that writeManCommands omits a hidden
// subcommand even when it belongs to a rendered group.
func TestManCommandsSkipsHiddenGrouped(t *testing.T) {
	root := &cobra.Command{Use: "iq"}
	root.AddGroup(&cobra.Group{ID: cmdGroupSources, Title: "Sources:"})
	vis := &cobra.Command{Use: "visiblecmd", Short: "v", GroupID: cmdGroupSources, Run: func(*cobra.Command, []string) {}}
	hid := &cobra.Command{Use: "secretcmd", Short: "s", GroupID: cmdGroupSources, Hidden: true, Run: func(*cobra.Command, []string) {}}
	root.AddCommand(vis, hid)

	var b strings.Builder
	writeManCommands(&b, root)
	out := b.String()
	require.Contains(t, out, ".SS iq visiblecmd", "visible grouped command must render")
	require.NotContains(t, out, "secretcmd", "hidden grouped command must be omitted")
}

// TestManLocalFlagsSkipsHiddenAndHelp pins that writeManLocalFlags omits a hidden
// flag and the auto-added --help while rendering an ordinary one.
func TestManLocalFlagsSkipsHiddenAndHelp(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().String("visibleflag", "", "visible usage")
	cmd.Flags().String("secretflag", "", "secret usage")
	cmd.Flags().Lookup("secretflag").Hidden = true
	cmd.Flags().Bool("help", false, "help usage")

	var b strings.Builder
	writeManLocalFlags(&b, cmd)
	out := b.String()
	require.Contains(t, out, "visible usage", "ordinary flag must render")
	require.NotContains(t, out, "secret usage", "hidden flag must be omitted")
	require.NotContains(t, out, "\\-\\-help", "auto-added --help must be omitted")
}

// TestManFlagDefaultSuffix pins writeManFlag's default-suffix rule: a meaningful
// default is appended, but the zero-ish defaults "", "false", and "0" are not.
func TestManFlagDefaultSuffix(t *testing.T) {
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fs.Int("count", 0, "count usage")                // DefValue "0"
	fs.Duration("wait", 5*time.Second, "wait usage") // DefValue "5s"
	fs.Bool("toggle", false, "toggle usage")         // DefValue "false"
	fs.String("name", "", "name usage")              // DefValue ""
	fs.String("mode", "auto", "mode usage")          // DefValue "auto"

	tests := []struct {
		flag       string
		wantSuffix string // "" means no "(default ...)" suffix at all
	}{
		{"count", ""},
		{"wait", "(default 5s)"},
		{"toggle", ""},
		{"name", ""},
		{"mode", "(default auto)"},
	}
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			var b strings.Builder
			writeManFlag(&b, fs.Lookup(tt.flag))
			out := b.String()
			if tt.wantSuffix == "" {
				require.NotContains(t, out, "(default", "unexpected default suffix")
			} else {
				require.Contains(t, out, tt.wantSuffix)
			}
		})
	}
}

// TestIsIndentedBlock pins that both a space- and a tab-indented first line mark a
// preformatted block, while unindented prose does not.
func TestIsIndentedBlock(t *testing.T) {
	require.True(t, isIndentedBlock("  spaced example"))
	require.True(t, isIndentedBlock("\ttabbed example"))
	require.False(t, isIndentedBlock("ordinary prose"))
}

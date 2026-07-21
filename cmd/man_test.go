package cmd

import (
	"os"
	"strings"
	"testing"

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
	// Auto-added help/completion commands are deliberately omitted.
	require.NotContains(t, out, ".SS iq completion")
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

package cmd

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// demoScriptPath locates the recorded demo's expect script from the cmd package
// directory, the way skillPath reaches ../skills/iq/SKILL.md.
const demoScriptPath = "../scripts/demo/basic.exp"

// demoEnteredLine matches one command line the demo enters. basic.exp spells every
// one as `type {iq …}` or `recall {…}` on a line of its own, with nothing computed,
// so this is the whole grammar: the recording enters literals, and the literals are
// what this test resolves.
var demoEnteredLine = regexp.MustCompile(`(?m)^(type|recall) \{(.*)\}$`)

// demoCommandTokens returns the command lines basic.exp enters, in order. A `type`
// line is the command; a `recall` line presses the up arrow and appends its suffix,
// so what the shell runs is the previous line with that suffix on the end, and that
// is the line this returns.
func demoCommandTokens(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile(demoScriptPath)
	require.NoError(t, err, "read the demo script")

	var out []string
	for _, m := range demoEnteredLine.FindAllStringSubmatch(strings.ReplaceAll(string(raw), "\r\n", "\n"), -1) {
		verb, text := m[1], m[2]
		if verb == "recall" {
			require.NotEmpty(t, out, "recall %q has no earlier typed line to recall", text)
			out = append(out, out[len(out)-1]+text)
			continue
		}
		require.True(t, strings.HasPrefix(text, "iq "), "typed line %q is not an iq command", text)
		out = append(out, text)
	}
	return out
}

// demoIQHead returns the part of an entered line iq itself runs: everything up to
// the first pipe outside quotes. The plan is paged (`… --explain -v -C | less -R`),
// and only the head of that line is iq's, the tail is the shell's and has no CLI
// surface to drift. The cut has to be quote-aware because the demo's jq filter
// carries pipes of its own inside single quotes, and cutting at the first `|` of all
// would hand cobra half a filter.
func demoIQHead(line string) string {
	var quote rune
	for i, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '|':
			return strings.TrimSpace(line[:i])
		}
	}
	return strings.TrimSpace(line)
}

// TestDemoIQHead proves the cut above splits on the shell's pipe and only on it: a
// pipe inside the jq filter's quotes is part of the command, not a cut point.
func TestDemoIQHead(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "no pipe at all",
			line: `iq '.["2"]'`,
			want: `iq '.["2"]'`,
		},
		{
			name: "every pipe is quoted",
			line: `iq '.[] | select(.year > 2015) | {title, price}'`,
			want: `iq '.[] | select(.year > 2015) | {title, price}'`,
		},
		{
			name: "quoted pipes then a shell pipe",
			line: `iq '.[] | select(.year > 2015) | {title, price}' --explain -v -C | less -R`,
			want: `iq '.[] | select(.year > 2015) | {title, price}' --explain -v -C`,
		},
		{
			name: "a pipe inside double quotes inside single ones",
			line: `iq '.[] | select(.author | test("Martin|Kleppmann"))' -g | head`,
			want: `iq '.[] | select(.author | test("Martin|Kleppmann"))' -g`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, demoIQHead(tt.line))
		})
	}
}

// TestDemoCommandsResolve is the drift guard for docs/docs/assets/demo.svg: every
// command line the recording enters is walked against the tree newRootCmd builds,
// so a renamed flag or subcommand fails here instead of being recorded into an SVG
// that shows a command iq no longer accepts. The stamp gate catches the same drift
// only after the file has changed; this catches it in the unit suite.
func TestDemoCommandsResolve(t *testing.T) {
	root, _ := newRootCmd()

	lines := demoCommandTokens(t)
	require.GreaterOrEqual(t, len(lines), 5, "the demo's entered command lines stopped being extracted")

	for _, line := range lines {
		t.Run(line, func(t *testing.T) {
			require.NoError(t, resolveSkillToken(root, demoIQHead(line)), "demo command %q", line)
		})
	}
}

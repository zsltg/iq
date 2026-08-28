package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// skillPath locates the agent skill from the cmd package directory, the way
// TestManPageMatchesCommitted reads ../docs/man/iq.1. The skill is hand-written
// (nothing generates it), so the guard here is not a golden comparison: it
// checks that the file still parses as an Agent Skill and that every command
// line it teaches still resolves against the real cobra tree.
const skillPath = "../skills/iq/SKILL.md"

// skillFrontmatter is the subset of the Agent Skills frontmatter this repo sets.
type skillFrontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
}

// readSkill splits the committed skill into its parsed frontmatter and its body,
// failing the test if the frontmatter block is missing or malformed.
func readSkill(t *testing.T) (skillFrontmatter, string) {
	t.Helper()

	raw, err := os.ReadFile(skillPath)
	require.NoError(t, err, "read the committed skill")

	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	require.True(t, strings.HasPrefix(text, "---\n"), "skill must open with a --- frontmatter line")

	rest := strings.TrimPrefix(text, "---\n")
	end := strings.Index(rest, "\n---\n")
	require.GreaterOrEqual(t, end, 0, "frontmatter must close with a --- line")

	var fm skillFrontmatter
	require.NoError(t, yaml.Unmarshal([]byte(rest[:end+1]), &fm), "frontmatter must parse as YAML")

	return fm, rest[end+len("\n---\n"):]
}

// TestSkillFrontmatter pins the fields the Agent Skills format requires and the
// size limits a loader enforces: the description is what an agent reads to
// decide whether to load the skill, so an empty or overlong one breaks
// discovery.
func TestSkillFrontmatter(t *testing.T) {
	fm, _ := readSkill(t)

	tests := []struct {
		name   string
		value  string
		want   string
		maxLen int
	}{
		{"name is iq", fm.Name, "iq", 0},
		{"description present and within the 1024-character limit", fm.Description, "", 1024},
		{"license present", fm.License, "", 0},
		{"compatibility present and within the 500-character limit", fm.Compatibility, "", 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotEmpty(t, tt.value)
			if tt.want != "" {
				require.Equal(t, tt.want, tt.value)
			}
			if tt.maxLen > 0 {
				require.LessOrEqual(t, len(tt.value), tt.maxLen)
			}
		})
	}
}

// TestSkillNameMatchesDirectory pins the spec's layout rule: the skill lives at
// skills/<name>/SKILL.md, so the frontmatter name and the directory agree.
func TestSkillNameMatchesDirectory(t *testing.T) {
	fm, _ := readSkill(t)
	require.Equal(t, filepath.Base(filepath.Dir(skillPath)), fm.Name)
}

// TestSkillBodyStaysShort keeps the body inside the format's recommended size,
// so the whole skill still fits an agent's context alongside the task.
func TestSkillBodyStaysShort(t *testing.T) {
	_, body := readSkill(t)
	require.Less(t, strings.Count(body, "\n"), 500, "skill body must stay under 500 lines")
}

// TestSkillCommandsResolve is the drift guard: every command line the skill
// teaches is walked against the tree newRootCmd builds, so a renamed flag or
// subcommand fails here instead of misleading an agent.
func TestSkillCommandsResolve(t *testing.T) {
	root, _ := newRootCmd()
	_, body := readSkill(t)

	tokens := skillCommandTokens(body)
	require.GreaterOrEqual(t, len(tokens), 20, "the skill's command tokens stopped being extracted")

	for _, token := range tokens {
		t.Run(token, func(t *testing.T) {
			require.NoError(t, resolveSkillToken(root, token), "skill token %q", token)
		})
	}
}

// TestSkillTokenCheckerRejects proves the checker above fails on the shapes it
// is there to catch: an unknown flag, an unknown subcommand, a flag left without
// its value, and a flag borrowed from another command.
func TestSkillTokenCheckerRejects(t *testing.T) {
	root, _ := newRootCmd()

	tests := []struct {
		name  string
		token string
		want  string
	}{
		{"unknown root flag", "iq --no-such-flag", `unknown flag "--no-such-flag"`},
		{"unknown subcommand", "iq nosuch sub", `unknown command "nosuch"`},
		{"flag without its value", "iq add --store", `flag "--store" expects a value`},
		{"flag of another command", "iq ls --key-field id", `unknown flag "--key-field"`},
		{"unbalanced quote", "iq --src 'shop", "unbalanced quote"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := resolveSkillToken(root, tt.token)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

// skillCodeSpan matches one backtick-delimited span of the skill body.
var skillCodeSpan = regexp.MustCompile("`[^`]+`")

// skillCommandTokens returns the code spans that claim to be iq usage: a command
// line starting with `iq`, or a bare flag fragment. Everything else in backticks
// is jq, JSON, a file name or a URI query, and carries no CLI surface to drift.
func skillCommandTokens(body string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, span := range skillCodeSpan.FindAllString(body, -1) {
		token := strings.Trim(span, "`")
		if token != "iq" && !strings.HasPrefix(token, "iq ") && !strings.HasPrefix(token, "-") {
			continue
		}
		if seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

// skillWord is one shell-style word of a token; a quoted word is always an
// operand (a jq filter, a URI), never a subcommand name.
type skillWord struct {
	text   string
	quoted bool
}

// splitSkillWords splits a token the way a shell would, keeping a single- or
// double-quoted span as one word so `select(.a == "b")` does not fragment.
func splitSkillWords(token string) ([]skillWord, error) {
	var (
		words  []skillWord
		cur    strings.Builder
		open   bool
		quoted bool
		quote  rune
	)
	flush := func() {
		if open {
			words = append(words, skillWord{text: cur.String(), quoted: quoted})
		}
		cur.Reset()
		open, quoted = false, false
	}
	for _, r := range token {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote, open, quoted = r, true, true
		case unicode.IsSpace(r):
			flush()
		default:
			open = true
			cur.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unbalanced quote in %q", token)
	}
	flush()
	return words, nil
}

// resolveSkillToken walks one token against the cobra tree. A token opening with
// `iq` walks from the root, so a flag must be known on the command reached so
// far or inherited from its parents and a bare word must name a subcommand until
// the first operand; a bare flag fragment carries no command context, so its
// flags are looked up across the whole tree.
func resolveSkillToken(root *cobra.Command, token string) error {
	words, err := splitSkillWords(token)
	if err != nil {
		return err
	}
	if len(words) == 0 {
		return nil
	}

	cur := root
	i := 0
	switch {
	case !words[0].quoted && words[0].text == "iq":
		i = 1
	case strings.HasPrefix(words[0].text, "-"):
		cur = nil
	default:
		return nil
	}

	// A token that is nothing but a flag name mentions the flag in prose, so it
	// carries no value; every longer fragment is an invocation and must.
	mention := cur == nil && len(words) == 1

	operand := false
	for ; i < len(words); i++ {
		w := words[i]
		switch {
		case !w.quoted && (w.text == "--help" || w.text == "-h" || w.text == "help"):
			// Cobra's built-in help flag and help command.
		case !w.quoted && len(w.text) > 1 && strings.HasPrefix(w.text, "-"):
			name, inline, _ := strings.Cut(strings.TrimLeft(w.text, "-"), "=")
			flag := findSkillFlag(root, cur, name)
			if flag == nil {
				return fmt.Errorf("unknown flag %q", w.text)
			}
			if inline == "" && !mention && flag.Value.Type() != "bool" {
				if i+1 >= len(words) {
					return fmt.Errorf("flag %q expects a value", w.text)
				}
				i++ // The next word is this flag's value.
			}
		case isSkillPlaceholder(w.text):
			// <handle>, <filter>: the reader substitutes it.
		case !w.quoted && !operand && cur != nil && cur.HasAvailableSubCommands():
			sub := findSkillCommand(cur, w.text)
			if sub == nil {
				return fmt.Errorf("unknown command %q", w.text)
			}
			cur = sub
		default:
			operand = true
		}
	}
	return nil
}

// isSkillPlaceholder reports whether a word is a <placeholder> the reader fills in.
func isSkillPlaceholder(word string) bool {
	return strings.HasPrefix(word, "<") && strings.HasSuffix(word, ">")
}

// findSkillCommand returns the named subcommand of cmd, or nil.
func findSkillCommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, sub := range cmd.Commands() {
		if sub.Name() == name || sub.HasAlias(name) {
			return sub
		}
	}
	return nil
}

// findSkillFlag resolves a flag name or shorthand on cmd, including what cmd
// inherits from its parents. With no command context (a bare flag fragment) it
// searches the whole tree, since the fragment names no command to scope it to.
func findSkillFlag(root, cmd *cobra.Command, name string) *pflag.Flag {
	if cmd != nil {
		return lookupSkillFlag(cmd, name)
	}
	for _, c := range append([]*cobra.Command{root}, allSkillCommands(root)...) {
		if flag := lookupSkillFlag(c, name); flag != nil {
			return flag
		}
	}
	return nil
}

// lookupSkillFlag looks one name or shorthand up in a command's local,
// persistent and inherited flag sets.
func lookupSkillFlag(cmd *cobra.Command, name string) *pflag.Flag {
	for _, set := range []*pflag.FlagSet{cmd.Flags(), cmd.PersistentFlags(), cmd.InheritedFlags()} {
		if len(name) == 1 {
			if flag := set.ShorthandLookup(name); flag != nil {
				return flag
			}
			continue
		}
		if flag := set.Lookup(name); flag != nil {
			return flag
		}
	}
	return nil
}

// allSkillCommands flattens the command tree below root.
func allSkillCommands(root *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, sub := range root.Commands() {
		out = append(out, sub)
		out = append(out, allSkillCommands(sub)...)
	}
	return out
}

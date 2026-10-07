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
	var s skillSplitter
	for _, r := range token {
		s.feed(r)
	}
	if s.quote != 0 {
		return nil, fmt.Errorf("unbalanced quote in %q", token)
	}
	s.flush()
	return s.words, nil
}

// skillSplitter holds the state of splitSkillWords: the words so far, the word
// being read, and the quote that is open.
type skillSplitter struct {
	words        []skillWord
	cur          strings.Builder
	open, quoted bool
	quote        rune
}

// feed reads one rune.
func (s *skillSplitter) feed(r rune) {
	switch {
	case s.quote != 0:
		if r == s.quote {
			s.quote = 0
			return
		}
		s.cur.WriteRune(r)
	case r == '\'' || r == '"':
		s.quote, s.open, s.quoted = r, true, true
	case unicode.IsSpace(r):
		s.flush()
	default:
		s.open = true
		s.cur.WriteRune(r)
	}
}

// flush ends the current word, when one is open.
func (s *skillSplitter) flush() {
	if s.open {
		s.words = append(s.words, skillWord{text: s.cur.String(), quoted: s.quoted})
	}
	s.cur.Reset()
	s.open, s.quoted = false, false
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
	w, ok := startSkillWalk(root, words)
	if !ok {
		return nil
	}
	for ; w.i < len(w.words); w.i++ {
		if err := w.step(); err != nil {
			return err
		}
	}
	return nil
}

// skillWalk is the state of one token walk against the cobra tree.
type skillWalk struct {
	root, cur *cobra.Command // cur is nil for a bare flag fragment
	words     []skillWord
	i         int  // index of the word that the walk reads next
	mention   bool // the token is only a flag name in prose
	operand   bool // a word was already read that is not a subcommand
}

// startSkillWalk decides where a token starts. It reports false when the token is
// neither an iq command nor a flag fragment.
func startSkillWalk(root *cobra.Command, words []skillWord) (*skillWalk, bool) {
	w := &skillWalk{root: root, cur: root, words: words}
	switch {
	case !words[0].quoted && words[0].text == "iq":
		w.i = 1
	case strings.HasPrefix(words[0].text, "-"):
		w.cur = nil
	default:
		return nil, false
	}
	// A token that is nothing but a flag name mentions the flag in prose, so it
	// carries no value; every longer fragment is an invocation and must.
	w.mention = w.cur == nil && len(words) == 1
	return w, true
}

// step reads the word at w.i. A flag that takes a value moves w.i past the value.
func (w *skillWalk) step() error {
	word := w.words[w.i]
	switch {
	case isSkillHelp(word):
		// Cobra's built-in help flag and help command.
		return nil
	case isSkillFlag(word):
		return w.flag()
	case isSkillPlaceholder(word.text):
		// <handle>, <filter>: the reader substitutes it.
		return nil
	case w.wantsSubcommand(word):
		return w.subcommand()
	default:
		w.operand = true
		return nil
	}
}

// flag checks the flag word at w.i. It skips the next word when that word is the
// value of the flag.
func (w *skillWalk) flag() error {
	text := w.words[w.i].text
	name, inline, _ := strings.Cut(strings.TrimLeft(text, "-"), "=")
	flag := findSkillFlag(w.root, w.cur, name)
	if flag == nil {
		return fmt.Errorf("unknown flag %q", text)
	}
	if inline == "" && !w.mention && flag.Value.Type() != "bool" {
		if w.i+1 >= len(w.words) {
			return fmt.Errorf("flag %q expects a value", text)
		}
		w.i++ // The next word is this flag's value.
	}
	return nil
}

// subcommand moves the walk into the subcommand that the word at w.i names.
func (w *skillWalk) subcommand() error {
	text := w.words[w.i].text
	sub := findSkillCommand(w.cur, text)
	if sub == nil {
		return fmt.Errorf("unknown command %q", text)
	}
	w.cur = sub
	return nil
}

// isSkillFlag reports whether a word is an unquoted flag. A lone dash is not.
func isSkillFlag(word skillWord) bool {
	return !word.quoted && len(word.text) > 1 && strings.HasPrefix(word.text, "-")
}

// wantsSubcommand reports whether the word is read as a subcommand name: it is
// unquoted, no operand came before it, and the current command has subcommands.
func (w *skillWalk) wantsSubcommand(word skillWord) bool {
	return !word.quoted && !w.operand && w.cur != nil && w.cur.HasAvailableSubCommands()
}

// isSkillHelp reports whether a word is cobra's help flag or help command.
func isSkillHelp(word skillWord) bool {
	return !word.quoted && (word.text == "--help" || word.text == "-h" || word.text == "help")
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

// TestSkillTokenCheckerAccepts pins the shapes the checker lets through, so a
// change to its walk cannot make it stricter or looser without a failing row.
func TestSkillTokenCheckerAccepts(t *testing.T) {
	root, _ := newRootCmd()

	tests := []struct {
		name  string
		token string
	}{
		{"bare iq", "iq"},
		{"long help flag", "iq --help"},
		{"short help flag", "iq ls -h"},
		{"help command", "iq help"},
		{"inherited bool flag", "iq ls -v"},
		{"short flag with a value", "iq add -n shop"},
		{"inline value", "iq add --handle=shop"},
		{"quoted operand then a flag", `iq '.[] | select(.a == "b")' --src shop`},
		{"placeholder", "iq add <uri>"},
		{"nested subcommands", "iq config keyring ls"},
		{"bare flag mention", "--reveal"},
		{"bare fragment with a value", "--src shop"},
		{"flag value that looks like a flag", "--src --reveal"},
		{"not an iq token", "jq .a"},
		{"empty token", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, resolveSkillToken(root, tt.token))
		})
	}
}

// TestSkillTokenCheckerRejectsMore adds the reject shapes of nested commands,
// short flags and flags that follow an operand.
func TestSkillTokenCheckerRejectsMore(t *testing.T) {
	root, _ := newRootCmd()

	tests := []struct {
		name  string
		token string
		want  string
	}{
		{"unknown nested subcommand", "iq config nosuch", `unknown command "nosuch"`},
		{"short flag without its value", "iq add -n", `flag "-n" expects a value`},
		{"unknown bare flag", "--nosuch shop", `unknown flag "--nosuch"`},
		{"flag of another command after an operand", "iq ls extra --key-field id", `unknown flag "--key-field"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := resolveSkillToken(root, tt.token)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestSplitSkillWords pins how a token splits into words and which words count
// as quoted.
func TestSplitSkillWords(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  []skillWord
	}{
		{"plain words", "iq ls -v", []skillWord{{"iq", false}, {"ls", false}, {"-v", false}}},
		{"single quotes", "iq 'a b'", []skillWord{{"iq", false}, {"a b", true}}},
		{"double quotes", `iq "a b"`, []skillWord{{"iq", false}, {"a b", true}}},
		{"quote inside a word", "x'a b'y", []skillWord{{"xa by", true}}},
		{"other quote inside quotes", `'say "hi"'`, []skillWord{{`say "hi"`, true}}},
		{"adjacent quoted words", "'a' 'b'", []skillWord{{"a", true}, {"b", true}}},
		{"empty quoted word", "iq ''", []skillWord{{"iq", false}, {"", true}}},
		{"extra spaces", "  iq   ls  ", []skillWord{{"iq", false}, {"ls", false}}},
		{"empty token", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splitSkillWords(tt.token)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}

	_, err := splitSkillWords(`iq "open`)
	require.ErrorContains(t, err, "unbalanced quote")
}

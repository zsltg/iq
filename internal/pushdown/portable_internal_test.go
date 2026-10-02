package pushdown

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPortableRegex(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{"empty matches every string in both engines", "", true},
		{"literals", "abc", true},
		{"anchors, dot, star", "^A.*z$", true},
		{"quantifiers including bounds", "a+b?c{2,3}", true},
		{"group and alternation", "(foo|bar)", true},
		{"character class with a range", "[a-z_]", true},
		{"negated class", "[^0-9]", true},
		{"ASCII shorthand classes (S excluded, see below)", `\d\w\s\D\W`, true},
		{"word boundaries", `\bword\B`, true},
		{"escaped punctuation is a literal", `a\.b\*c\\d`, true},
		{"a '}' or '(' not forming a rejected construct", "a}b(c", true},
		{"a trailing '(' (no lookahead) is left to the engine", "a(", true},
		{"a trailing '[' (not a POSIX class) is left to the engine", "a[", true},
		{"a trailing portable escape", `x\d`, true},
		{"an escaped paren is a literal, so the question mark only quantifies it", `\(?`, true},
		{"lookahead", "(?=x)", false},
		{"non-capturing group extension", "(?:x)", false},
		{"POSIX class", "[[:alpha:]]", false},
		{"possessive star", "a*+", false},
		{"possessive plus", "a++", false},
		{"possessive question", "a?+", false},
		{"possessive bound", "a{2}+", false},
		{"dangling backslash", `x\`, false},
		{"backreference", `\1`, false},
		{"unicode property", `\p{L}`, false},
		{"engine-specific escape", `\h`, false},
		{"engine-specific escape inside a class", `[\p{L}]`, false},
		{"RE2 S matches a vertical tab PCRE's does not — narrows", `\S`, false},
		{"the same escape mid-pattern", `x\Sy`, false},
		{"a group extension truncated to the end of the pattern", "(?", false},
		{"a POSIX class truncated to the end of the pattern", "[[", false},
		{"an inline flag group after a portable escape", `\d(?i)`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, portableRegex(tt.pattern))
		})
	}
}

func TestPortableEscape(t *testing.T) {
	// Every whitelisted escape letter is portable. \S is deliberately absent: RE2's
	// \s omits the vertical tab PCRE's includes, so RE2's \S matches it and PCRE's
	// does not, and pushing \S to a PCRE backend would narrow the match. \s stays
	// because it diverges the other way (a superset the client re-run corrects).
	// The rejected bytes pin the alphanumeric range checks. Escaped punctuation is a
	// literal in both engines. The bytes next to each alphanumeric range edge are
	// portable: '@' before 'A', '[' after 'Z', '`' before 'a', '{' after 'z', '/'
	// before '0', ':' after '9'. A byte at 0x80 or above is not an ASCII letter or
	// digit, so it is portable too.
	tests := []struct {
		name string
		b    byte
		want bool
	}{
		{"whitelisted letter d", 'd', true},
		{"whitelisted letter D", 'D', true},
		{"whitelisted letter w", 'w', true},
		{"whitelisted letter W", 'W', true},
		{"whitelisted letter s", 's', true},
		{"whitelisted letter b", 'b', true},
		{"whitelisted letter B", 'B', true},
		{"whitelisted letter n", 'n', true},
		{"whitelisted letter t", 't', true},
		{"whitelisted letter r", 'r', true},
		{"whitelisted letter f", 'f', true},
		{"whitelisted letter v", 'v', true},
		{"not whitelisted letter or digit S", 'S', false},
		{"not whitelisted letter or digit a", 'a', false},
		{"not whitelisted letter or digit A", 'A', false},
		{"not whitelisted letter or digit z", 'z', false},
		{"not whitelisted letter or digit Z", 'Z', false},
		{"not whitelisted letter or digit 0", '0', false},
		{"not whitelisted letter or digit 9", '9', false},
		{"not whitelisted letter or digit g", 'g', false},
		{"not whitelisted letter or digit h", 'h', false},
		{"escaped punctuation .", '.', true},
		{"escaped punctuation *", '*', true},
		{"escaped punctuation +", '+', true},
		{"escaped punctuation ?", '?', true},
		{"escaped punctuation (", '(', true},
		{"escaped punctuation )", ')', true},
		{"escaped punctuation [", '[', true},
		{"escaped punctuation ]", ']', true},
		{"escaped punctuation {", '{', true},
		{"escaped punctuation }", '}', true},
		{"escaped punctuation ^", '^', true},
		{"escaped punctuation $", '$', true},
		{"escaped punctuation |", '|', true},
		{"escaped punctuation \\", '\\', true},
		{"escaped punctuation /", '/', true},
		{"escaped punctuation -", '-', true},
		{"range edge @ before A", '@', true},
		{"range edge [ after Z", '[', true},
		{"range edge ` before a", '`', true},
		{"range edge { after z", '{', true},
		{"range edge / before 0", '/', true},
		{"range edge : after 9", ':', true},
		{"range edge 0x80 above ASCII", 0x80, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, portableEscape(tt.b))
		})
	}
}

package pushdown

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPortableRegex(t *testing.T) {
	portable := []string{
		"",           // empty matches every string in both engines
		"abc",        // literals
		"^A.*z$",     // anchors, dot, star
		"a+b?c{2,3}", // quantifiers including bounds
		"(foo|bar)",  // group and alternation
		"[a-z_]",     // character class with a range
		"[^0-9]",     // negated class
		`\d\w\s\D\W`, // ASCII shorthand classes (\S excluded — see below)
		`\bword\B`,   // word boundaries
		`a\.b\*c\\d`, // escaped punctuation is a literal
		"a}b(c",      // a '}' or '(' not forming a rejected construct
		"a(",         // a trailing '(' (no lookahead) is left to the engine
		"a[",         // a trailing '[' (not a POSIX class) is left to the engine
		`x\d`,        // a trailing portable escape
		`\(?`,        // an escaped paren is a literal, so the `?` only quantifies it
	}
	for _, p := range portable {
		require.True(t, portableRegex(p), "should be portable: %q", p)
	}

	notPortable := []string{
		"(?=x)",       // lookahead
		"(?:x)",       // non-capturing group extension
		"[[:alpha:]]", // POSIX class
		"a*+",         // possessive star
		"a++",         // possessive plus
		"a?+",         // possessive question
		"a{2}+",       // possessive bound
		`x\`,          // dangling backslash
		`\1`,          // backreference
		`\p{L}`,       // unicode property
		`\h`,          // engine-specific escape
		`[\p{L}]`,     // engine-specific escape inside a class
		`\S`,          // RE2 \S matches a vertical tab PCRE's does not — narrows
		`x\Sy`,        // the same escape mid-pattern
		"(?",          // a group extension truncated to the end of the pattern
		"[[",          // a POSIX class truncated to the end of the pattern
		`\d(?i)`,      // an inline flag group after a portable escape
	}
	for _, p := range notPortable {
		require.False(t, portableRegex(p), "should not be portable: %q", p)
	}
}

func TestPortableEscape(t *testing.T) {
	// Every whitelisted escape letter is portable. \S is deliberately absent: RE2's
	// \s omits the vertical tab PCRE's includes, so RE2's \S matches it and PCRE's
	// does not — pushing \S to a PCRE backend would narrow the match. \s stays
	// because it diverges the other way (a superset the client re-run corrects).
	for _, b := range []byte("dDwWsbBntrfv") {
		require.True(t, portableEscape(b), "escape should be portable: \\%c", b)
	}
	// \S and boundary letters/digits that are not whitelisted must be rejected,
	// which pins the alphanumeric range checks.
	for _, b := range []byte("SaAzZ09gh") {
		require.False(t, portableEscape(b), "escape should not be portable: \\%c", b)
	}
	// Escaped punctuation is a literal in both engines.
	for _, b := range []byte(`.*+?()[]{}^$|\/-`) {
		require.True(t, portableEscape(b), "escaped punctuation should be portable: \\%c", b)
	}
}

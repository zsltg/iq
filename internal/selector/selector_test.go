package selector_test

import (
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/selector"
)

func TestKeysBounded(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want []string
	}{
		{"single field", ".key1", []string{"key1"}},
		{"string index with colon", `.["book:1"]`, []string{"book:1"}},
		{"quoted dotted field", `."a.b"`, []string{"a.b"}},
		{"leading index then suffix", ".key1.name", []string{"key1"}},
		{"array of two fields", "[ .key1, .key2 ]", []string{"key1", "key2"}},
		{"array of field paths", "[ .key1.name, .key2.name ]", []string{"key1", "key2"}},
		{"pipe uses only left as key", ".key1 | .name", []string{"key1"}},
		{"comma feeds both root", ".a, .b", []string{"a", "b"}},
		{"arithmetic feeds both root", ".a + .b", []string{"a", "b"}},
		{"parens preserve root", "(.a, .b).name", []string{"a", "b"}},
		{"object values feed root", "{x: .a, y: .b}", []string{"a", "b"}},
		{"object shorthand implies same key", "{a, b}", []string{"a", "b"}},
		{"object string shorthand implies key", `{"foo"}`, []string{"foo"}},
		{"mixed shorthand and explicit value", "{a, y: .b}", []string{"a", "b"}},
		{"unary term", "-.a", []string{"a"}},
		{"interpolation queries", `"\(.a)-\(.b)"`, []string{"a", "b"}},
		{"duplicates collapse in order", "[ .a, .a.x, .a ]", []string{"a"}},
		{"plain literal has no key", `"hi"`, nil},
		{"empty array has no key", "[]", nil},
		{"variable shorthand is not a key", "{$x}", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := gojq.Parse(tt.expr)
			require.NoError(t, err)

			got := selector.Keys(q)

			require.False(t, got.Scan, "expression must be bounded, not a scan")
			require.Equal(t, tt.want, got.Keys)
		})
	}
}

func TestKeysScan(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"bare identity", "."},
		{"root iteration", ".[]"},
		{"recurse", ".."},
		{"keys builtin", "keys"},
		{"to_entries builtin", "to_entries"},
		{"map builtin", "map(.x)"},
		{"variable index", ".[$k]"},
		{"computed index", ".[.a]"},
		{"slice index", ".[1:2]"},
		{"interpolated index", `.["\(.x)"]`},
		{"variable reference", "$x"},
		{"control flow", "if .a then .b end"},
		{"scan wins over explicit key", "[ ., .a ]"},
		{"unresolvable inside pipe left", ".[] | .name"},
		{"unresolvable inside array", "[ .a, keys ]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := gojq.Parse(tt.expr)
			require.NoError(t, err)

			got := selector.Keys(q)

			require.True(t, got.Scan, "expression must classify as a scan")
			require.Empty(t, got.Keys)
		})
	}
}

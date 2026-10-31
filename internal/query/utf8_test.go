package query_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestUTF8GuardKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{"ascii", "user:1", ""},
		{"empty", "", ""},
		{"multibyte", "kéy\U0001F600", ""},
		{"lone continuation byte", "a\x80b", `record key "a\x80b" is not valid UTF-8`},
		{"truncated sequence", "\xe2\x82", `record key "\xe2\x82" is not valid UTF-8`},
		{"surrogate", "\xed\xa0\x80", `record key "\xed\xa0\x80" is not valid UTF-8`},
		{"0xff", "\xff", `record key "\xff" is not valid UTF-8`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := query.UTF8Guard{Driver: "elasticsearch"}.Key(tt.key)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "elasticsearch: "+tt.want)
			require.ErrorContains(t, err, "U+FFFD")
		})
	}
}

func TestUTF8GuardKeys(t *testing.T) {
	require.NoError(t, query.UTF8Guard{Driver: "couchdb"}.Keys(nil))
	require.NoError(t, query.UTF8Guard{Driver: "couchdb"}.Keys([]string{"a", "b"}))
	err := query.UTF8Guard{Driver: "couchdb"}.Keys([]string{"a", "b\xff", "c\xfe"})
	require.ErrorContains(t, err, `couchdb: record key "b\xff" is not valid UTF-8`)
}

// TestUTF8GuardValueValidAllocatesNothing pins the fast path: a valid record is
// checked without building a field path or sorting the names.
func TestUTF8GuardValueValidAllocatesNothing(t *testing.T) {
	value := map[string]any{"b": "x", "a": []any{"y", map[string]any{"c": "z", "d": 1.0}}}
	allocs := testing.AllocsPerRun(100, func() {
		if err := (query.UTF8Guard{Driver: "test"}).Value("k", value); err != nil {
			panic(err)
		}
	})
	require.Zero(t, allocs)
}

func TestUTF8GuardValue(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"nil", nil, ""},
		{"number", 1.5, ""},
		{"valid string", "héllo", ""},
		{"empty string", "", ""},
		{"valid object", map[string]any{"a": []any{"x", map[string]any{"b": "y"}}}, ""},
		{"string slice is not walked", []string{"\xff"}, ""},
		{"top-level string", "a\xffb", `the value holds a string that is not valid UTF-8`},
		{"object value", map[string]any{"a": "\xff"}, `field "a" holds a string that is not valid UTF-8`},
		{"object name", map[string]any{"\xff": 1}, `the field name "\xff" is not valid UTF-8`},
		{"nested name", map[string]any{"a": map[string]any{"b\xfe": 1}}, `the field name "a.b\xfe" is not valid UTF-8`},
		{"array element", map[string]any{"a": []any{"ok", "\xff"}}, `field "a[1]" holds a string`},
		{"array in array", []any{[]any{"\xff"}}, `field "[0][0]" holds a string`},
		{"object in array", map[string]any{"a": []any{map[string]any{"b": "\xff"}}}, `field "a[0].b" holds a string`},
		{"the lowest name wins", map[string]any{"z": "\xff", "a": "\xfe", "m": "\xfd"}, `field "a" holds a string`},
		{"a bad name before a bad value", map[string]any{"b": "\xff", "a\xff": 1}, `the field name "a\xff"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := query.UTF8Guard{Driver: "couchdb"}.Value("k", tt.value)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "couchdb: key \"k\": ")
			require.ErrorContains(t, err, tt.want)
			require.ErrorContains(t, err, "U+FFFD")
		})
	}
}

func TestUTF8GuardKeyValue(t *testing.T) {
	g := query.UTF8Guard{Driver: "couchdb"}
	require.NoError(t, g.KeyValue("k", map[string]any{"a": "b"}))
	// A bad key wins over a bad value, so the order of the checks is fixed.
	err := g.KeyValue("k\xff", map[string]any{"a": "\xfe"})
	require.ErrorContains(t, err, `couchdb: record key "k\xff" is not valid UTF-8`)
	err = g.KeyValue("k", map[string]any{"a": "\xfe"})
	require.ErrorContains(t, err, `couchdb: key "k": field "a" holds a string`)
}

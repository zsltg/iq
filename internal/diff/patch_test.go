package diff_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/diff"
)

func TestPatch(t *testing.T) {
	tests := []struct {
		name string
		a, b any
		want string
	}{
		{
			name: "identical is the empty patch",
			a:    map[string]any{"x": 1},
			b:    map[string]any{"x": 1},
			want: `[]`,
		},
		{
			name: "added key",
			a:    map[string]any{},
			b:    map[string]any{"x": 1},
			want: `[{"op":"add","path":"/x","value":1}]`,
		},
		{
			name: "removed key",
			a:    map[string]any{"x": 1},
			b:    map[string]any{},
			want: `[{"op":"remove","path":"/x"}]`,
		},
		{
			name: "replaced value",
			a:    map[string]any{"x": 1},
			b:    map[string]any{"x": 2},
			want: `[{"op":"replace","path":"/x","value":2}]`,
		},
		{
			// RFC 6901: "/" in a key escapes to "~1" in the JSON Pointer.
			name: "slash in key is escaped",
			a:    map[string]any{"a/b": 1},
			b:    map[string]any{"a/b": 2},
			want: `[{"op":"replace","path":"/a~1b","value":2}]`,
		},
		{
			// RFC 6901: "~" in a key escapes to "~0".
			name: "tilde in key is escaped",
			a:    map[string]any{"a~b": 1},
			b:    map[string]any{"a~b": 2},
			want: `[{"op":"replace","path":"/a~0b","value":2}]`,
		},
		{
			// Compare marshals through encoding/json, so an int and a float64 of the
			// same magnitude round-trip to the same JSON and produce no op.
			name: "int and float of same magnitude is empty",
			a:    map[string]any{"n": 1},
			b:    map[string]any{"n": 1.0},
			want: `[]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := diff.Patch(tt.a, tt.b)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(raw))
		})
	}
}

func TestPatchMarshalError(t *testing.T) {
	// A channel cannot be JSON-marshaled, so Compare fails and Patch wraps it
	// rather than returning a half-built patch.
	_, err := diff.Patch(map[string]any{"c": make(chan int)}, map[string]any{})
	require.Error(t, err)
	require.ErrorContains(t, err, "json patch")
	// %w keeps the json cause reachable for errors.As; %v would sever it.
	var ute *json.UnsupportedTypeError
	require.ErrorAs(t, err, &ute)
}

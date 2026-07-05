package diff_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/diff"
)

func TestInfer(t *testing.T) {
	t.Run("object items report field types and presence", func(t *testing.T) {
		items := map[string]any{
			"1": map[string]any{"name": "a", "age": 30},
			"2": map[string]any{"name": "b"},
		}
		got := diff.Infer(items)
		require.Equal(t, map[string]any{"types": []any{"object"}}, got["$root"])
		require.Equal(t, map[string]any{"types": []any{"string"}, "presence": "2/2"}, got[".name"])
		require.Equal(t, map[string]any{"types": []any{"number"}, "presence": "1/2"}, got[".age"])
	})

	t.Run("nested objects flatten to dotted paths", func(t *testing.T) {
		items := map[string]any{
			"1": map[string]any{"addr": map[string]any{"city": "A"}},
		}
		got := diff.Infer(items)
		require.Equal(t, map[string]any{"types": []any{"object"}, "presence": "1/1"}, got[".addr"])
		require.Equal(t, map[string]any{"types": []any{"string"}, "presence": "1/1"}, got[".addr.city"])
	})

	t.Run("mixed root types collect distinct sorted types", func(t *testing.T) {
		items := map[string]any{
			"a": "plain string",
			"b": []any{1, 2},
			"c": map[string]any{"k": 1},
		}
		got := diff.Infer(items)
		require.Equal(t, map[string]any{"types": []any{"array", "object", "string"}}, got["$root"])
	})

	t.Run("a field with two types records both", func(t *testing.T) {
		items := map[string]any{
			"1": map[string]any{"v": 1},
			"2": map[string]any{"v": "x"},
		}
		got := diff.Infer(items)
		require.Equal(t, map[string]any{"types": []any{"number", "string"}, "presence": "2/2"}, got[".v"])
	})
}

func TestInferFeedsTree(t *testing.T) {
	a := diff.Infer(map[string]any{"1": map[string]any{"name": "x"}})
	b := diff.Infer(map[string]any{"1": map[string]any{"name": "x", "email": "y"}})
	changes := diff.Tree(a, b)
	require.Equal(t, []diff.Change{
		{Path: []string{".email"}, Op: diff.OpAdd, New: map[string]any{"types": []any{"string"}, "presence": "1/1"}},
	}, changes)
}

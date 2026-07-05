package diff_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/diff"
)

func TestTree(t *testing.T) {
	tests := []struct {
		name string
		a, b any
		want []diff.Change
	}{
		{
			name: "identical scalars",
			a:    "x",
			b:    "x",
			want: nil,
		},
		{
			name: "changed scalar",
			a:    "x",
			b:    "y",
			want: []diff.Change{{Path: []string{}, Op: diff.OpChange, Old: "x", New: "y"}},
		},
		{
			name: "int and float of same magnitude are equal",
			a:    map[string]any{"n": 1},
			b:    map[string]any{"n": 1.0},
			want: nil,
		},
		{
			name: "added and removed map keys",
			a:    map[string]any{"keep": 1, "gone": 2},
			b:    map[string]any{"keep": 1, "new": 3},
			want: []diff.Change{
				{Path: []string{"gone"}, Op: diff.OpRemove, Old: 2},
				{Path: []string{"new"}, Op: diff.OpAdd, New: 3},
			},
		},
		{
			name: "nested changed value keeps its path",
			a:    map[string]any{"addr": map[string]any{"city": "A"}},
			b:    map[string]any{"addr": map[string]any{"city": "B"}},
			want: []diff.Change{{Path: []string{"addr", "city"}, Op: diff.OpChange, Old: "A", New: "B"}},
		},
		{
			name: "array element change and trailing add",
			a:    []any{1, 2},
			b:    []any{1, 9, 3},
			want: []diff.Change{
				{Path: []string{"[1]"}, Op: diff.OpChange, Old: 2, New: 9},
				{Path: []string{"[2]"}, Op: diff.OpAdd, New: 3},
			},
		},
		{
			name: "array shrink removes trailing elements",
			a:    []any{1, 2, 3},
			b:    []any{1},
			want: []diff.Change{
				{Path: []string{"[1]"}, Op: diff.OpRemove, Old: 2},
				{Path: []string{"[2]"}, Op: diff.OpRemove, Old: 3},
			},
		},
		{
			name: "type mismatch is one whole-subtree change",
			a:    map[string]any{"v": map[string]any{"a": 1}},
			b:    map[string]any{"v": "scalar"},
			want: []diff.Change{{Path: []string{"v"}, Op: diff.OpChange, Old: map[string]any{"a": 1}, New: "scalar"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, diff.Tree(tt.a, tt.b))
		})
	}
}

func TestTreeDeterministicKeyOrder(t *testing.T) {
	a := map[string]any{"b": 1, "a": 1, "c": 1}
	b := map[string]any{"b": 2, "a": 2, "c": 2}
	got := diff.Tree(a, b)
	require.Len(t, got, 3)
	require.Equal(t, []string{"a"}, got[0].Path)
	require.Equal(t, []string{"b"}, got[1].Path)
	require.Equal(t, []string{"c"}, got[2].Path)
}

func TestKeyed(t *testing.T) {
	tests := []struct {
		name string
		a, b map[string]any
		want []diff.ItemDelta
	}{
		{
			name: "identical sets",
			a:    map[string]any{"1": map[string]any{"v": 1}},
			b:    map[string]any{"1": map[string]any{"v": 1}},
			want: nil,
		},
		{
			name: "add remove change sorted by key",
			a:    map[string]any{"keep": map[string]any{"v": 1}, "gone": map[string]any{"v": 2}},
			b:    map[string]any{"keep": map[string]any{"v": 9}, "new": map[string]any{"v": 3}},
			want: []diff.ItemDelta{
				{Key: "gone", Op: diff.OpRemove, Old: map[string]any{"v": 2}},
				{Key: "keep", Op: diff.OpChange, Changes: []diff.Change{{Path: []string{"v"}, Op: diff.OpChange, Old: 1, New: 9}}},
				{Key: "new", Op: diff.OpAdd, New: map[string]any{"v": 3}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, diff.Keyed(tt.a, tt.b))
		})
	}
}

func TestSummarize(t *testing.T) {
	t.Run("items", func(t *testing.T) {
		ds := []diff.ItemDelta{
			{Key: "a", Op: diff.OpAdd},
			{Key: "b", Op: diff.OpRemove},
			{Key: "c", Op: diff.OpChange},
			{Key: "d", Op: diff.OpAdd},
		}
		require.Equal(t, diff.Summary{Added: 2, Removed: 1, Changed: 1}, diff.SummarizeItems(ds))
		require.False(t, diff.SummarizeItems(ds).Empty())
	})
	t.Run("empty", func(t *testing.T) {
		require.True(t, diff.SummarizeItems(nil).Empty())
		require.True(t, diff.Summary{}.Empty())
	})
	t.Run("changes", func(t *testing.T) {
		cs := []diff.Change{{Op: diff.OpAdd}, {Op: diff.OpChange}}
		require.Equal(t, diff.Summary{Added: 1, Changed: 1}, diff.SummarizeChanges(cs))
	})
}

func TestOpMarshalJSON(t *testing.T) {
	tests := []struct {
		op   diff.Op
		want string
	}{
		{diff.OpAdd, `"add"`},
		{diff.OpRemove, `"remove"`},
		{diff.OpChange, `"change"`},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			b, err := tt.op.MarshalJSON()
			require.NoError(t, err)
			require.Equal(t, tt.want, string(b))
		})
	}
}

func TestOpStringAndSymbol(t *testing.T) {
	tests := []struct {
		op       diff.Op
		str, sym string
	}{
		{diff.OpAdd, "add", "+"},
		{diff.OpRemove, "remove", "-"},
		{diff.OpChange, "change", "~"},
	}
	for _, tt := range tests {
		t.Run(tt.str, func(t *testing.T) {
			require.Equal(t, tt.str, tt.op.String())
			require.Equal(t, tt.sym, tt.op.Symbol())
		})
	}
}

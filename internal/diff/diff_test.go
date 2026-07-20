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
		{
			// LCS aligns [2,3] onto b, so the front insertion is a single Add at
			// [0] rather than the positional walk's cascade at every later index.
			name: "front insertion is a single add",
			a:    []any{2, 3},
			b:    []any{1, 2, 3},
			want: []diff.Change{{Path: []string{"[0]"}, Op: diff.OpAdd, New: 1}},
		},
		{
			// The deleted middle element aligns out, so it is a single Remove; the
			// trailing 3 anchors and reports nothing.
			name: "mid deletion is a single remove",
			a:    []any{1, 2, 3},
			b:    []any{1, 3},
			want: []diff.Change{{Path: []string{"[1]"}, Op: diff.OpRemove, Old: 2}},
		},
		{
			// No shared subsequence, so the two elements pair positionally and the
			// change recurses into the object: the delta lands at [0].v, not on the
			// whole element.
			name: "changed element between anchors recurses",
			a:    []any{map[string]any{"v": 1}},
			b:    []any{map[string]any{"v": 2}},
			want: []diff.Change{{Path: []string{"[0]", "v"}, Op: diff.OpChange, Old: 1, New: 2}},
		},
		{
			// An insertion before an anchor shifts the reported index of the later
			// paired change: add 0 at [0], then 2->9 lands at [2] in the aligned
			// overlay (1 anchors at [1]), pinning the i+adds index arithmetic.
			name: "insertion before a change pins the aligned index",
			a:    []any{1, 2, 3},
			b:    []any{0, 1, 9, 3},
			want: []diff.Change{
				{Path: []string{"[0]"}, Op: diff.OpAdd, New: 0},
				{Path: []string{"[2]"}, Op: diff.OpChange, Old: 2, New: 9},
			},
		},
		{
			// A paired block of two changed elements after an anchor pins the
			// recursion offsets (a[ai+k], b[bi+k]) at k=1 with ai,bi > 0: a lost or
			// negated offset walks the anchor against a later element and reports a
			// whole-subtree change instead of these field-level deltas.
			name: "anchored paired block recurses at the right offsets",
			a:    []any{true, map[string]any{"v": 1}, map[string]any{"w": 1}},
			b:    []any{true, map[string]any{"v": 2}, map[string]any{"w": 2}},
			want: []diff.Change{
				{Path: []string{"[1]", "v"}, Op: diff.OpChange, Old: 1, New: 2},
				{Path: []string{"[2]", "w"}, Op: diff.OpChange, Old: 1, New: 2},
			},
		},
		{
			// An equal map must anchor across an insertion: if the anchor
			// predicate stops recognizing equal maps, the map pairs with the
			// inserted scalar instead and the single Add becomes a change pair.
			name: "equal map anchors across an insertion",
			a:    []any{map[string]any{"v": 1}},
			b:    []any{0, map[string]any{"v": 1}},
			want: []diff.Change{{Path: []string{"[0]"}, Op: diff.OpAdd, New: 0}},
		},
		{
			// The nested-array analog: an equal inner array must anchor across an
			// insertion the same way.
			name: "equal nested array anchors across an insertion",
			a:    []any{[]any{1}},
			b:    []any{0, []any{1}},
			want: []diff.Change{{Path: []string{"[0]"}, Op: diff.OpAdd, New: 0}},
		},
		{
			// A scalar and an empty map are not the same: the anchor predicate's
			// one-sided type rejects must hold even when the composite side is
			// empty (a lost reject makes the nil range-loop vacuously agree).
			name: "scalar and empty map pair as a change",
			a:    []any{5},
			b:    []any{map[string]any{}},
			want: []diff.Change{{Path: []string{"[0]"}, Op: diff.OpChange, Old: 5, New: map[string]any{}}},
		},
		{
			name: "empty map and scalar pair as a change",
			a:    []any{map[string]any{}},
			b:    []any{5},
			want: []diff.Change{{Path: []string{"[0]"}, Op: diff.OpChange, Old: map[string]any{}, New: 5}},
		},
		{
			name: "scalar and empty array pair as a change",
			a:    []any{5},
			b:    []any{[]any{}},
			want: []diff.Change{{Path: []string{"[0]"}, Op: diff.OpChange, Old: 5, New: []any{}}},
		},
		{
			name: "empty array and scalar pair as a change",
			a:    []any{[]any{}},
			b:    []any{5},
			want: []diff.Change{{Path: []string{"[0]"}, Op: diff.OpChange, Old: []any{}, New: 5}},
		},
		{
			// A map that is a strict subset of the other must NOT anchor: the size
			// fast-reject in the anchor predicate is what keeps {a:1} and
			// {a:1,b:2} paired-and-recursed instead of silently equal.
			name: "subset map does not anchor",
			a:    []any{map[string]any{"a": 1}},
			b:    []any{map[string]any{"a": 1, "b": 2}},
			want: []diff.Change{{Path: []string{"[0]", "b"}, Op: diff.OpAdd, New: 2}},
		},
		{
			// Aligning [1,1,1] with [2,1] exercises the skip-a branch of the DP
			// fill: its value must flow into the table for the backtrack to pick
			// the two-delta alignment.
			name: "duplicate run against shorter array",
			a:    []any{1, 1, 1},
			b:    []any{2, 1},
			want: []diff.Change{
				{Path: []string{"[0]"}, Op: diff.OpChange, Old: 1, New: 2},
				{Path: []string{"[1]"}, Op: diff.OpRemove, Old: 1},
			},
		},
		{
			// The mirror shape exercises the skip-b branch of the DP fill: the
			// trailing 1 anchors and both 2s read as leading adds.
			name: "shorter array against duplicate run",
			a:    []any{1},
			b:    []any{2, 2, 1},
			want: []diff.Change{
				{Path: []string{"[0]"}, Op: diff.OpAdd, New: 2},
				{Path: []string{"[1]"}, Op: diff.OpAdd, New: 2},
			},
		},
		{
			// int/float64 parity holds through the LCS anchor predicate: 2 and 2.0
			// anchor, so only the genuine string change survives.
			name: "int and float anchor together in arrays",
			a:    []any{1, 2, "x"},
			b:    []any{1, 2.0, "y"},
			want: []diff.Change{{Path: []string{"[2]"}, Op: diff.OpChange, Old: "x", New: "y"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, diff.Tree(tt.a, tt.b))
		})
	}
}

func TestTreeSetArrays(t *testing.T) {
	opts := diff.Options{SetArrays: true}
	tests := []struct {
		name string
		a, b any
		want []diff.Change
	}{
		{
			name: "pure reorder is no delta",
			a:    []any{1, 2},
			b:    []any{2, 1},
			want: nil,
		},
		{
			// Elements are bucketed by canonical JSON, so the string "1" and the
			// int 1 are different members ("1" vs 1); a fmt-style key would
			// collide them into a spurious match.
			name: "string and number of the same spelling differ",
			a:    []any{"1"},
			b:    []any{1},
			want: []diff.Change{
				{Path: []string{}, Op: diff.OpRemove, Old: "1"},
				{Path: []string{}, Op: diff.OpAdd, New: 1},
			},
		},
		{
			// Multiset counts duplicates: the left has one extra 1, so exactly one
			// Remove, at the array's own (index-less) path.
			name: "duplicate count difference is one remove",
			a:    []any{1, 1, 2},
			b:    []any{1, 2},
			want: []diff.Change{{Path: []string{}, Op: diff.OpRemove, Old: 1}},
		},
		{
			// Membership delta both ways, emitted sorted by canonical key.
			name: "mixed add and remove sorted by canonical key",
			a:    []any{1, 2},
			b:    []any{2, 3},
			want: []diff.Change{
				{Path: []string{}, Op: diff.OpRemove, Old: 1},
				{Path: []string{}, Op: diff.OpAdd, New: 3},
			},
		},
		{
			// No pairing, so a nested array is frozen inside its canonical key:
			// [1,2] and [2,1] are distinct members, one removed and one added.
			name: "order stays significant inside a serialized element",
			a:    []any{[]any{1, 2}},
			b:    []any{[]any{2, 1}},
			want: []diff.Change{
				{Path: []string{}, Op: diff.OpRemove, Old: []any{1, 2}},
				{Path: []string{}, Op: diff.OpAdd, New: []any{2, 1}},
			},
		},
		{
			// Canonical marshal collapses 1 and 1.0, so same-magnitude elements are
			// the same member.
			name: "int and float of same magnitude are one member",
			a:    []any{1},
			b:    []any{1.0},
			want: nil,
		},
		{
			// Set semantics apply at every array the walk reaches through a map, and
			// the delta carries the array's field path with no index segment.
			name: "applies to an array nested in a map",
			a:    map[string]any{"x": []any{1, 1}},
			b:    map[string]any{"x": []any{1}},
			want: []diff.Change{{Path: []string{"x"}, Op: diff.OpRemove, Old: 1}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, diff.TreeOpt(tt.a, tt.b, opts))
		})
	}
}

func TestKeyedOptThreadsSetArrays(t *testing.T) {
	a := map[string]any{"k": map[string]any{"arr": []any{1, 2}}}
	b := map[string]any{"k": map[string]any{"arr": []any{2, 1}}}

	// The default aligns the reordered array by LCS, so the item reads as changed.
	require.Equal(t, []diff.ItemDelta{
		{Key: "k", Op: diff.OpChange, Changes: []diff.Change{
			{Path: []string{"arr", "[0]"}, Op: diff.OpRemove, Old: 1},
			{Path: []string{"arr", "[2]"}, Op: diff.OpAdd, New: 1},
		}},
	}, diff.Keyed(a, b))

	// SetArrays threads through the per-item tree, so a pure reorder is no delta.
	require.Nil(t, diff.KeyedOpt(a, b, diff.Options{SetArrays: true}))
}

func TestTreeArrayMemoryGuard(t *testing.T) {
	seq := func(n, extra int) []any {
		out := make([]any, 0, n+extra)
		for i := 0; i < extra; i++ {
			out = append(out, -1-i)
		}
		for i := 0; i < n; i++ {
			out = append(out, i)
		}
		return out
	}
	t.Run("exactly at the cap still aligns by LCS", func(t *testing.T) {
		// 1024*1024 = 1<<20 cells, exactly the cap and so not over it: two
		// equal-length arrays sharing a 1023-element run align to a single Remove at
		// [0] and a single Add. The removed head and the 1023 anchors each hold one
		// aligned slot, so the trailing Add lands at [1024]. The positional fallback
		// would instead report all 1024 indexes as changed, so this pins the strict
		// ">" boundary.
		a := append([]any{"L"}, seq(1023, 0)...)
		b := append(seq(1023, 0), "R")
		got := diff.Tree(a, b)
		require.Equal(t, []diff.Change{
			{Path: []string{"[0]"}, Op: diff.OpRemove, Old: "L"},
			{Path: []string{"[1024]"}, Op: diff.OpAdd, New: "R"},
		}, got)
	})
	t.Run("over the cap falls back to the positional walk", func(t *testing.T) {
		// 1100*1101 = 1211100 cells, above the cap: the positional fallback compares
		// by raw index, so the same front insertion cascades to one delta per index
		// plus a trailing Add — proving the guard engaged.
		a := seq(1100, 0)
		b := seq(1100, 1)
		got := diff.Tree(a, b)
		require.Len(t, got, 1101)
		require.Equal(t, []string{"[0]"}, got[0].Path)
		require.Equal(t, diff.OpChange, got[0].Op)
		require.Equal(t, []string{"[1100]"}, got[1100].Path)
		require.Equal(t, diff.OpAdd, got[1100].Op)
	})
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

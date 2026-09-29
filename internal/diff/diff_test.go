package diff_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
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
			// Numeric parity must survive inside an anchored composite: {v:1} and
			// {v:1.0} are the same map, so they anchor across the insertion. A
			// predicate that fell back to a plain deep comparison would treat the
			// int and the float64 as different and pair the map with the inserted
			// scalar instead.
			name: "equal map anchors across an insertion despite int/float spelling",
			a:    []any{map[string]any{"v": 1}},
			b:    []any{0, map[string]any{"v": 1.0}},
			want: []diff.Change{{Path: []string{"[0]"}, Op: diff.OpAdd, New: 0}},
		},
		{
			// The nested-array analog of the same parity rule.
			name: "equal nested array anchors across an insertion despite int/float spelling",
			a:    []any{[]any{1}},
			b:    []any{0, []any{1.0}},
			want: []diff.Change{{Path: []string{"[0]"}, Op: diff.OpAdd, New: 0}},
		},
		{
			// Anchoring compares every element of a nested array, not just the
			// first: these inner arrays agree at index 0 and differ at index 1, so
			// they must not anchor — the outer arrays pair positionally instead.
			name: "nested array anchoring compares beyond the first element",
			a:    []any{[]any{1, 2}},
			b:    []any{0, []any{1, 3}},
			want: []diff.Change{
				{Path: []string{"[0]"}, Op: diff.OpChange, Old: []any{1, 2}, New: 0},
				{Path: []string{"[1]"}, Op: diff.OpAdd, New: []any{1, 3}},
			},
		},
		{
			// A nested array that is a prefix of the other is not equal to it:
			// the left inner array matches the first element of the right one,
			// but the lengths differ, so they must not anchor.
			name: "nested array anchoring compares lengths",
			a:    []any{[]any{1}},
			b:    []any{0, []any{1, 2}},
			want: []diff.Change{
				{Path: []string{"[0]"}, Op: diff.OpChange, Old: []any{1}, New: 0},
				{Path: []string{"[1]"}, Op: diff.OpAdd, New: []any{1, 2}},
			},
		},
		{
			// The map analog: agreeing on one key is not enough to anchor.
			name: "map anchoring compares every key",
			a:    []any{map[string]any{"x": 1, "y": 2}},
			b:    []any{0, map[string]any{"x": 1, "y": 3}},
			want: []diff.Change{
				{Path: []string{"[0]"}, Op: diff.OpChange, Old: map[string]any{"x": 1, "y": 2}, New: 0},
				{Path: []string{"[1]"}, Op: diff.OpAdd, New: map[string]any{"x": 1, "y": 3}},
			},
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
		for i := range extra {
			out = append(out, -1-i)
		}
		for i := range n {
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
		// plus a trailing Add — proving the guard engaged. The surplus-right delta
		// is compared whole, Op and New together: an Add that reported the index but
		// dropped the value it added would satisfy a path-and-op-only assertion.
		a := seq(1100, 0)
		b := seq(1100, 1)
		got := diff.Tree(a, b)
		require.Len(t, got, 1101)
		require.Equal(t, diff.Change{Path: []string{"[0]"}, Op: diff.OpChange, Old: 0, New: -1}, got[0])
		require.Equal(t, diff.Change{Path: []string{"[1100]"}, Op: diff.OpAdd, New: 1099}, got[1100])
	})
	t.Run("over the cap reports the surplus left as removes", func(t *testing.T) {
		// The mirror of the case above: the longer side is the left one, so the
		// positional walk ends in the surplus-left arm instead. It is the only
		// caller that reaches that arm with the fallback engaged, and the delta is
		// compared whole so the removed value is pinned alongside its op.
		a := seq(1100, 1)
		b := seq(1100, 0)
		got := diff.Tree(a, b)
		require.Len(t, got, 1101)
		require.Equal(t, diff.Change{Path: []string{"[0]"}, Op: diff.OpChange, Old: -1, New: 0}, got[0])
		require.Equal(t, diff.Change{Path: []string{"[1100]"}, Op: diff.OpRemove, Old: 1099}, got[1100])
	})
}

func TestTreeDeterministicKeyOrder(t *testing.T) {
	// Enough keys that Go's randomized map iteration would have to hit the sorted
	// permutation by chance (1 in 20!) for an unsorted walk to look ordered, so
	// the assertion pins the sort rather than getting lucky on three keys.
	const keys = 20
	a := make(map[string]any, keys)
	b := make(map[string]any, keys)
	want := make([]string, 0, keys)
	for i := range keys {
		k := fmt.Sprintf("k%02d", i)
		a[k], b[k] = 1, 2
		want = append(want, k)
	}
	sort.Strings(want)

	got := diff.Tree(a, b)
	require.Len(t, got, keys)
	paths := make([]string, 0, len(got))
	for _, c := range got {
		require.Len(t, c.Path, 1)
		paths = append(paths, c.Path[0])
	}
	require.Equal(t, want, paths)
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

// TestSummaryEmptyNeedsEveryCount pins Empty as the conjunction of all three
// counters: one non-zero counter alone must make a summary non-empty, so a
// disjunction, a dropped conjunct, or a re-associated one is visible. A single
// all-zero and a single all-non-zero row cannot see any of that, since every
// rewriting of the guard agrees on those two.
func TestSummaryEmptyNeedsEveryCount(t *testing.T) {
	tests := []struct {
		name string
		s    diff.Summary
		want bool
	}{
		{name: "all zero is empty", s: diff.Summary{}, want: true},
		{name: "one add is not empty", s: diff.Summary{Added: 1}, want: false},
		{name: "one remove is not empty", s: diff.Summary{Removed: 1}, want: false},
		{name: "one change is not empty", s: diff.Summary{Changed: 1}, want: false},
		{name: "all three are not empty", s: diff.Summary{Added: 1, Removed: 1, Changed: 1}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.s.Empty())
		})
	}
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

// lcsLenOracle returns the length of a longest common subsequence of a and b,
// computed independently of the package under test: a forward DP over prefixes,
// where the production code fills a backward table over suffixes. Agreement
// between two differently-shaped implementations is what makes it an oracle.
func lcsLenOracle(a, b []int) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
				continue
			}
			dp[i][j] = max(dp[i-1][j], dp[i][j-1])
		}
	}
	return dp[len(a)][len(b)]
}

// enumerateArrays returns every array of length 0..maxLen over the alphabet
// 1..symbols, so a property holds over the whole small-input space rather than a
// handful of hand-picked rows.
func enumerateArrays(symbols, maxLen int) [][]int {
	out := [][]int{{}}
	frontier := [][]int{{}}
	for range maxLen {
		var next [][]int
		for _, prefix := range frontier {
			for s := 1; s <= symbols; s++ {
				grown := append(append([]int{}, prefix...), s)
				next = append(next, grown)
				out = append(out, grown)
			}
		}
		frontier = next
	}
	return out
}

// TestTreeArrayAlignmentIsMinimal pins the defining property of the LCS walk over
// every pair of small scalar arrays: the alignment is minimal. Anchors are the
// longest common subsequence, so the elements each side does NOT contribute to it
// are exactly the ones reported — changes+removes on the left, changes+adds on
// the right (a scalar pair reports one Change, never a nested fan-out). Any
// mutation that degrades the table, the backtrack, or its bounds yields a
// non-minimal alignment and fails here, which a fixed table of rows cannot pin.
func TestTreeArrayAlignmentIsMinimal(t *testing.T) {
	arrays := enumerateArrays(3, 4)
	for _, ai := range arrays {
		for _, bi := range arrays {
			a, b := make([]any, len(ai)), make([]any, len(bi))
			for i, v := range ai {
				a[i] = v
			}
			for i, v := range bi {
				b[i] = v
			}

			var adds, removes, changes int
			for _, c := range diff.Tree(a, b) {
				switch c.Op {
				case diff.OpAdd:
					adds++
				case diff.OpRemove:
					removes++
				case diff.OpChange:
					changes++
				}
			}

			lcs := lcsLenOracle(ai, bi)
			require.Equal(t, len(ai)-lcs, changes+removes, "left side not minimal for %v vs %v", ai, bi)
			require.Equal(t, len(bi)-lcs, changes+adds, "right side not minimal for %v vs %v", ai, bi)
		}
	}
}

// TestTreeSetArraysDeterministicOrder pins that set-mode deltas are emitted in
// canonical-key order. Enough distinct members are used that Go's randomized map
// iteration would have to land on the sorted permutation by chance for an
// unsorted walk to look ordered.
func TestTreeSetArraysDeterministicOrder(t *testing.T) {
	const members = 20
	a := make([]any, 0, members)
	want := make([]string, 0, members)
	for i := range members {
		v := fmt.Sprintf("m%02d", i)
		a = append(a, v)
		want = append(want, `"`+v+`"`)
	}
	sort.Strings(want)

	// Every member is left-only, so each one reports a Remove in canonical order.
	got := diff.TreeOpt(a, []any{}, diff.Options{SetArrays: true})
	require.Len(t, got, members)
	keys := make([]string, 0, len(got))
	for _, c := range got {
		require.Equal(t, diff.OpRemove, c.Op)
		bs, err := json.Marshal(c.Old)
		require.NoError(t, err)
		keys = append(keys, string(bs))
	}
	require.Equal(t, want, keys)
}

// TestTreeArrayAlignmentIsMinimalAtScale extends the minimality property past the
// sizes an exhaustive enumeration can reach. Arrays up to length 12 over four
// symbols expose a degraded DP table that small inputs hide: a table that is
// merely wrong rather than differently-tie-broken still finds a minimal path
// through a handful of elements, but not through a dozen. The seed is fixed, so
// a failure reproduces exactly.
func TestTreeArrayAlignmentIsMinimalAtScale(t *testing.T) {
	rng := rand.New(rand.NewSource(20260720)) //nolint:gosec // deterministic test input, not security
	for range 4000 {
		ai := randomInts(rng, rng.Intn(13), 4)
		bi := randomInts(rng, rng.Intn(13), 4)

		var adds, removes, changes int
		for _, ch := range diff.Tree(anySlice(ai), anySlice(bi)) {
			switch ch.Op {
			case diff.OpAdd:
				adds++
			case diff.OpRemove:
				removes++
			case diff.OpChange:
				changes++
			}
		}

		lcs := lcsLenOracle(ai, bi)
		require.Equal(t, len(ai)-lcs, changes+removes, "left side not minimal for %v vs %v", ai, bi)
		require.Equal(t, len(bi)-lcs, changes+adds, "right side not minimal for %v vs %v", ai, bi)
	}
}

// randomInts returns n values drawn from 1..symbols.
func randomInts(rng *rand.Rand, n, symbols int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = rng.Intn(symbols) + 1
	}
	return out
}

// anySlice widens a slice of ints to the normalized []any the differ walks.
func anySlice(xs []int) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

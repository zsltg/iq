// Package diff structurally compares two JSON-ready values — the normalized form
// (nil, bool, int, float64, string, []any, map[string]any) that every driver
// adapter produces. It is driver-agnostic and holds no I/O: callers read each
// side through the query ports and hand the materialized values here. Tree diffs
// two values; Keyed aligns two keyed item sets. Arrays are aligned by a longest
// common subsequence so a single insertion reports one delta rather than a
// cascade at every later index; TreeOpt/KeyedOpt with Options{SetArrays:true}
// switch arrays to order-insensitive multiset comparison instead. Patch (see
// patch.go) renders the same two values as an RFC 6902 JSON Patch. Inference of a
// comparable shape (projected to feed the same Tree) lives in the sibling
// internal/shape package.
package diff

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

// lcsCellCap bounds the longest-common-subsequence dynamic-programming table:
// above this many cells (len(a)·len(b)) walkSlice falls back to the positional
// index walk, trading alignment quality for bounded memory on pathologically
// large arrays. The assumption is that two arrays whose product exceeds ~1e6
// elements are rare in a diff and not worth an O(n·m) table.
const lcsCellCap = 1 << 20

// Options selects the comparison semantics a walk uses, so a caller picks a mode
// without a second entry point per mode. The zero value is the default
// order-sensitive, LCS-aligned comparison.
type Options struct {
	// SetArrays compares every array as an order-insensitive multiset (duplicates
	// counted) rather than aligning it positionally by LCS.
	SetArrays bool
}

// Op is the kind of a single delta: a value present only on the right (Add),
// only on the left (Remove), or differing between the two (Change).
type Op uint8

// The three delta kinds. The zero value is Add so an unset Op is never a silent
// Change.
const (
	OpAdd Op = iota
	OpRemove
	OpChange
)

// String returns the lowercase name of the op.
func (o Op) String() string {
	switch o {
	case OpAdd:
		return "add"
	case OpRemove:
		return "remove"
	case OpChange:
		return "change"
	default:
		return "unknown"
	}
}

// MarshalJSON renders the op as its name so structured output reads "add" rather
// than a bare integer.
func (o Op) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(o.String())), nil
}

// Symbol returns the one-character marker used in the human report: "+" added,
// "-" removed, "~" changed.
func (o Op) Symbol() string {
	switch o {
	case OpAdd:
		return "+"
	case OpRemove:
		return "-"
	case OpChange:
		return "~"
	default:
		return "?"
	}
}

// Change is one structural delta between two values, located at Path (the
// sequence of map keys and "[i]" array indexes from the root). Old is the
// left-hand value (nil for Add) and New the right-hand value (nil for Remove).
type Change struct {
	Path []string `json:"path"`
	Op   Op       `json:"op"`
	Old  any      `json:"old,omitempty"`
	New  any      `json:"new,omitempty"`
}

// Tree returns the deltas that turn a into b under the default options: maps
// compared key by key, arrays aligned by longest common subsequence, scalars by
// equality. A map-versus-array or map-versus-scalar mismatch is one Change of the
// whole subtree. The result is deterministic — map keys are visited in sorted
// order and array deltas in aligned-position order — so output and tests are
// stable.
func Tree(a, b any) []Change { return TreeOpt(a, b, Options{}) }

// TreeOpt is Tree under explicit Options: Options{SetArrays:true} compares every
// array as an order-insensitive multiset instead of aligning it by LCS.
func TreeOpt(a, b any, o Options) []Change {
	var out []Change
	walk(nil, a, b, &out, o)
	return out
}

// walk appends the deltas between a and b at path to out under o.
func walk(path []string, a, b any, out *[]Change, o Options) {
	am, aIsMap := a.(map[string]any)
	bm, bIsMap := b.(map[string]any)
	if aIsMap && bIsMap {
		walkMap(path, am, bm, out, o)
		return
	}
	as, aIsSlice := a.([]any)
	bs, bIsSlice := b.([]any)
	if aIsSlice && bIsSlice {
		if o.SetArrays {
			walkSet(path, as, bs, out)
			return
		}
		walkSlice(path, as, bs, out, o)
		return
	}
	if !equal(a, b) {
		*out = append(*out, Change{Path: clone(path), Op: OpChange, Old: a, New: b})
	}
}

// walkMap diffs two maps at path, visiting the union of keys in sorted order.
func walkMap(path []string, a, b map[string]any, out *[]Change, o Options) {
	for _, k := range unionKeys(a, b) {
		av, aok := a[k]
		bv, bok := b[k]
		child := append(path, k) //nolint:gocritic // child slice, cloned before storage
		switch {
		case aok && !bok:
			*out = append(*out, Change{Path: clone(child), Op: OpRemove, Old: av})
		case !aok && bok:
			*out = append(*out, Change{Path: clone(child), Op: OpAdd, New: bv})
		default:
			walk(child, av, bv, out, o)
		}
	}
}

// walkSlice diffs two arrays at path aligned by their longest common subsequence
// (lcsPairs): elements that appear in both, in order, are anchors and produce no
// delta. Between two anchors, the left and right leftovers are paired positionally
// and recursed (so a changed element reports its field-level deltas), then the
// surplus is Remove (left-only) or Add (right-only). Every emitted delta — anchor,
// paired change, Add, Remove — advances one aligned position, so a delta's index
// is its position in the LCS-aligned overlay of the two arrays, not its raw index
// in either side. An inserted element therefore shifts the reported index of the
// anchors after it (the "i + adds" convention): a front insertion is a single Add
// at [0] rather than a cascade. Above lcsCellCap DP cells it falls back to the
// positional index walk to bound memory.
func walkSlice(path []string, a, b []any, out *[]Change, o Options) {
	if len(a)*len(b) > lcsCellCap {
		walkPositional(path, a, b, out, o)
		return
	}
	pairs := lcsPairs(a, b)
	ai, bi, pos := 0, 0, 0
	block := func(pa, pb int) {
		la, lb := pa-ai, pb-bi
		common := min(la, lb)
		for k := 0; k < common; k++ {
			child := append(path, "["+strconv.Itoa(pos)+"]") //nolint:gocritic // cloned before storage
			walk(child, a[ai+k], b[bi+k], out, o)
			pos++
		}
		for k := common; k < la; k++ {
			child := append(path, "["+strconv.Itoa(pos)+"]") //nolint:gocritic // cloned before storage
			*out = append(*out, Change{Path: clone(child), Op: OpRemove, Old: a[ai+k]})
			pos++
		}
		for k := common; k < lb; k++ {
			child := append(path, "["+strconv.Itoa(pos)+"]") //nolint:gocritic // cloned before storage
			*out = append(*out, Change{Path: clone(child), Op: OpAdd, New: b[bi+k]})
			pos++
		}
	}
	for _, p := range pairs {
		block(p[0], p[1])
		ai, bi = p[0]+1, p[1]+1
		pos++ // the matched anchor occupies one aligned position and emits nothing
	}
	block(len(a), len(b)) // the tail leftover after the last anchor
}

// walkPositional diffs two arrays at path by raw index: the memory-bounded
// fallback for arrays too large for the LCS table. Trailing elements present on
// only one side are Add or Remove; overlapping indexes recurse.
func walkPositional(path []string, a, b []any, out *[]Change, o Options) {
	n := max(len(a), len(b))
	for i := 0; i < n; i++ {
		child := append(path, "["+strconv.Itoa(i)+"]") //nolint:gocritic // cloned before storage
		switch {
		case i >= len(b):
			*out = append(*out, Change{Path: clone(child), Op: OpRemove, Old: a[i]})
		case i >= len(a):
			*out = append(*out, Change{Path: clone(child), Op: OpAdd, New: b[i]})
		default:
			walk(child, a[i], b[i], out, o)
		}
	}
}

// walkSet diffs two arrays at path as multisets, order-insensitive with duplicates
// counted: each element is keyed by its canonical JSON serialization (Go sorts map
// keys, so the encoding is canonical, and an int and a float64 of the same
// magnitude encode identically). A key present more often on the left emits one
// Remove per surplus occurrence, more often on the right one Add per surplus
// occurrence; equal counts emit nothing, so a pure reorder is no delta. Without
// element identity there is no "changed element", only membership, so nothing
// recurses — a nested array inside an element keeps its order inside the serialized
// key. Deltas carry the array's own path with no index segment (indexes are
// meaningless order-insensitively) and are emitted sorted by canonical key.
func walkSet(path []string, a, b []any, out *[]Change) {
	left, right := multiset(a), multiset(b)
	for _, key := range sortedSetKeys(left.counts, right.counts) {
		delta := left.counts[key] - right.counts[key]
		for i := 0; i < delta; i++ {
			*out = append(*out, Change{Path: clone(path), Op: OpRemove, Old: left.sample[key]})
		}
		for i := 0; i < -delta; i++ {
			*out = append(*out, Change{Path: clone(path), Op: OpAdd, New: right.sample[key]})
		}
	}
}

// bag is a multiset of elements keyed by canonical serialization: counts holds the
// occurrence count per key, sample the first element seen for a key (any
// occurrence serves as the reported value, since equal keys are equal values).
type bag struct {
	counts map[string]int
	sample map[string]any
}

// multiset buckets xs by canonical serialization into a bag.
func multiset(xs []any) bag {
	b := bag{counts: make(map[string]int, len(xs)), sample: make(map[string]any, len(xs))}
	for _, x := range xs {
		key := canonicalKey(x)
		if b.counts[key] == 0 {
			b.sample[key] = x
		}
		b.counts[key]++
	}
	return b
}

// sortedSetKeys returns the sorted union of the keys of two count maps.
func sortedSetKeys(a, b map[string]int) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// canonicalKey serializes v to its canonical JSON form for multiset bucketing,
// falling back to fmt formatting when v cannot be marshaled (same spirit as the
// CLI's compact helper).
func canonicalKey(v any) string {
	if bs, err := json.Marshal(v); err == nil {
		return string(bs)
	}
	return fmt.Sprintf("%v", v)
}

// lcsPairs returns the index pairs (i in a, j in b) of a longest common
// subsequence of a and b, matched by same, in increasing order. The DP table over
// suffixes and its backtrack share the deterministic tie-break "advance a when the
// skip-a subsequence is at least as long as skip-b", so the alignment is stable.
func lcsPairs(a, b []any) [][2]int {
	la, lb := len(a), len(b)
	// dp[i][j] is the LCS length of a[i:] and b[j:]; the extra row and column are
	// the empty-suffix base case (length 0).
	dp := make([][]int, la+1)
	for i := range dp {
		dp[i] = make([]int, lb+1)
	}
	for i := la - 1; i >= 0; i-- {
		for j := lb - 1; j >= 0; j-- {
			if same(a[i], b[j]) {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var pairs [][2]int
	// Every iteration advances i or j by one, so the backtrack legitimately takes
	// at most la+lb steps; the explicit bound makes the loop terminate — with a
	// visibly wrong alignment — even if an advance is lost, rather than spin.
	for i, j, steps := 0, 0, 0; i < la && j < lb && steps < la+lb; steps++ {
		switch {
		case same(a[i], b[j]):
			pairs = append(pairs, [2]int{i, j})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	return pairs
}

// same reports whether two normalized values are deeply equal for LCS anchoring:
// maps by key, arrays by position, scalars via equal (so an int and a float64 of
// the same magnitude anchor together, matching the rest of the package).
func same(a, b any) bool {
	am, aIsMap := a.(map[string]any)
	bm, bIsMap := b.(map[string]any)
	if aIsMap || bIsMap {
		if !aIsMap || !bIsMap || len(am) != len(bm) {
			return false
		}
		for k, av := range am {
			bv, ok := bm[k]
			if !ok || !same(av, bv) {
				return false
			}
		}
		return true
	}
	as, aIsSlice := a.([]any)
	bs, bIsSlice := b.([]any)
	if aIsSlice || bIsSlice {
		if !aIsSlice || !bIsSlice || len(as) != len(bs) {
			return false
		}
		for i := range as {
			if !same(as[i], bs[i]) {
				return false
			}
		}
		return true
	}
	return equal(a, b)
}

// ItemDelta is the difference for one keyed item between two sets. Op Add and
// Remove carry the whole New or Old value; Op Change carries the field-level
// Changes within the item.
type ItemDelta struct {
	Key     string   `json:"key"`
	Op      Op       `json:"op"`
	Changes []Change `json:"changes,omitempty"`
	Old     any      `json:"old,omitempty"`
	New     any      `json:"new,omitempty"`
}

// Keyed aligns two keyed item sets under the default options and reports one
// ItemDelta per differing key, sorted by key. A key only in a is Remove, only in b
// is Add, and a key in both whose values differ is Change carrying the per-field
// Tree of that item. Keys whose values are equal produce no delta.
func Keyed(a, b map[string]any) []ItemDelta { return KeyedOpt(a, b, Options{}) }

// KeyedOpt is Keyed under explicit Options: the per-item Change tree of a changed
// key is computed with o, so Options{SetArrays:true} compares each item's arrays
// as multisets.
func KeyedOpt(a, b map[string]any, o Options) []ItemDelta {
	var out []ItemDelta
	for _, k := range unionKeys(a, b) {
		av, aok := a[k]
		bv, bok := b[k]
		switch {
		case aok && !bok:
			out = append(out, ItemDelta{Key: k, Op: OpRemove, Old: av})
		case !aok && bok:
			out = append(out, ItemDelta{Key: k, Op: OpAdd, New: bv})
		default:
			if ch := TreeOpt(av, bv, o); len(ch) > 0 {
				out = append(out, ItemDelta{Key: k, Op: OpChange, Changes: ch})
			}
		}
	}
	return out
}

// Summary counts deltas by kind, for a report footer or an exit-code decision.
type Summary struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
}

// Empty reports whether there were no differences.
func (s Summary) Empty() bool { return s.Added == 0 && s.Removed == 0 && s.Changed == 0 }

// SummarizeItems tallies a keyed diff by op.
func SummarizeItems(ds []ItemDelta) Summary {
	var s Summary
	for _, d := range ds {
		bump(&s, d.Op)
	}
	return s
}

// SummarizeChanges tallies a tree diff by op.
func SummarizeChanges(cs []Change) Summary {
	var s Summary
	for _, c := range cs {
		bump(&s, c.Op)
	}
	return s
}

// bump increments the field of s that op names.
func bump(s *Summary, op Op) {
	switch op {
	case OpAdd:
		s.Added++
	case OpRemove:
		s.Removed++
	case OpChange:
		s.Changed++
	}
}

// unionKeys returns the sorted union of the keys of a and b.
func unionKeys(a, b map[string]any) []string {
	seen := make(map[string]struct{}, len(a))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// equal reports whether two scalar (or mismatched-composite) values are equal.
// Numbers compare by value so an int and a float64 holding the same number match
// — the two adapters can normalize the same magnitude to different Go numeric
// types. Everything else falls back to a deep comparison.
func equal(a, b any) bool {
	if an, ok := asFloat(a); ok {
		bn, ok := asFloat(b)
		return ok && an == bn
	}
	if _, ok := asFloat(b); ok {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// asFloat returns v as a float64 when it is one of the normalized numeric types.
func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

// clone returns a fresh copy of path so appends into the shared backing array of
// a parent's slice never rewrite a stored delta's Path.
func clone(path []string) []string {
	out := make([]string, len(path))
	copy(out, path)
	return out
}

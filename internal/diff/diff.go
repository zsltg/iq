// Package diff structurally compares two JSON-ready values — the normalized form
// (nil, bool, int, float64, string, []any, map[string]any) that every driver
// adapter produces. It is driver-agnostic and holds no I/O: callers read each
// side through the query ports and hand the materialized values here. Tree diffs
// two values; Keyed aligns two keyed item sets; Infer (shape.go) reduces a set to
// a comparable shape so schema diff runs through the same Tree.
package diff

import (
	"reflect"
	"sort"
	"strconv"
)

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

// Tree returns the deltas that turn a into b, walked recursively: maps compared
// key by key, arrays by index, scalars by equality. A map-versus-array or
// map-versus-scalar mismatch is one Change of the whole subtree. The result is
// deterministic — map keys are visited in sorted order — so output and tests are
// stable.
func Tree(a, b any) []Change {
	var out []Change
	walk(nil, a, b, &out)
	return out
}

// walk appends the deltas between a and b at path to out.
func walk(path []string, a, b any, out *[]Change) {
	am, aIsMap := a.(map[string]any)
	bm, bIsMap := b.(map[string]any)
	if aIsMap && bIsMap {
		walkMap(path, am, bm, out)
		return
	}
	as, aIsSlice := a.([]any)
	bs, bIsSlice := b.([]any)
	if aIsSlice && bIsSlice {
		walkSlice(path, as, bs, out)
		return
	}
	if !equal(a, b) {
		*out = append(*out, Change{Path: clone(path), Op: OpChange, Old: a, New: b})
	}
}

// walkMap diffs two maps at path, visiting the union of keys in sorted order.
func walkMap(path []string, a, b map[string]any, out *[]Change) {
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
			walk(child, av, bv, out)
		}
	}
}

// walkSlice diffs two arrays at path by index. Trailing elements present on only
// one side are Add or Remove; overlapping indexes recurse.
func walkSlice(path []string, a, b []any, out *[]Change) {
	n := max(len(a), len(b))
	for i := 0; i < n; i++ {
		child := append(path, "["+strconv.Itoa(i)+"]") //nolint:gocritic // cloned before storage
		switch {
		case i >= len(b):
			*out = append(*out, Change{Path: clone(child), Op: OpRemove, Old: a[i]})
		case i >= len(a):
			*out = append(*out, Change{Path: clone(child), Op: OpAdd, New: b[i]})
		default:
			walk(child, a[i], b[i], out)
		}
	}
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

// Keyed aligns two keyed item sets and reports one ItemDelta per differing key,
// sorted by key. A key only in a is Remove, only in b is Add, and a key in both
// whose values differ is Change carrying the per-field Tree of that item. Keys
// whose values are equal produce no delta.
func Keyed(a, b map[string]any) []ItemDelta {
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
			if ch := Tree(av, bv); len(ch) > 0 {
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

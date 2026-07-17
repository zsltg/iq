package shape_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/shape"
)

// mapCorpus builds items where each instance carries field as an object; the i-th
// entry of instances lists that instance's child keys, each mapped to a fixed
// {"v":1} value. It gives precise control over the object count (len(instances)),
// the distinct child keys, and the key-slots (Σ len), so a threshold can be
// pinned exactly.
func mapCorpus(field string, instances [][]string) map[string]any {
	items := make(map[string]any, len(instances))
	for i, keys := range instances {
		obj := make(map[string]any, len(keys))
		for _, k := range keys {
			obj[k] = map[string]any{"v": 1}
		}
		items[strconv.Itoa(i)] = map[string]any{field: obj}
	}
	return items
}

// nKeys returns key0..key(n-1).
func nKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = "key" + strconv.Itoa(i)
	}
	return keys
}

// singleKeyInstances returns one instance per key in keys (each instance holds
// exactly that one key), so objCount == len(keys) == slots.
func singleKeyInstances(keys []string) [][]string {
	out := make([][]string, len(keys))
	for i, k := range keys {
		out[i] = []string{k}
	}
	return out
}

func mapTypeOf(items map[string]any, path string) []any {
	return shape.Infer(items).Comparable()[path].(map[string]any)["types"].([]any)
}

// TestInferMapsCorpus is the E3 four-way sanity check: a wide fixed record, an
// id-keyed map, an enum-ish status, and a free-text field classify correctly
// under the 8/8/0.75 defaults.
func TestInferMapsCorpus(t *testing.T) {
	items := map[string]any{}
	fixed := []string{"a", "b", "c", "d", "e", "f", "g", "h"} // 8 fixed fields, repeated every instance
	for i := 0; i < 20; i++ {
		rec := make(map[string]any, len(fixed))
		for _, k := range fixed {
			rec[k] = 1
		}
		items[strconv.Itoa(i)] = map[string]any{
			"rec":    rec,
			"seenBy": map[string]any{"user" + strconv.Itoa(i): map[string]any{"when": "t"}}, // one unique id per instance
			"status": []string{"active", "inactive", "pending"}[i%3],
			"desc":   "free text number " + strconv.Itoa(i), // unique per instance
		}
	}
	got := shape.Infer(items).Comparable()

	require.Equal(t, []any{"object"}, got[".rec"].(map[string]any)["types"], "a wide fixed record stays an object")
	require.Contains(t, got, ".rec.a", "fixed record keeps its literal child paths")
	require.Equal(t, []any{"map"}, got[".seenBy"].(map[string]any)["types"], "an id-keyed sub-object collapses to a map")
	require.Contains(t, got, ".seenBy{}.when", "the map value shape is projected under {}")
	require.NotContains(t, got, ".seenBy.user0", "id-keyed literal paths are dropped")
	require.Equal(t, []any{"string"}, got[".status"].(map[string]any)["types"], "a scalar enum-ish field is not a map")
	require.Equal(t, []any{"string"}, got[".desc"].(map[string]any)["types"], "a free-text field is not a map")

	// The collapsed map value is an object typed only (no presence), and its own
	// field carries required presence against the summed object count.
	require.Equal(t, map[string]any{"types": []any{"object"}}, got[".seenBy{}"], "the map value node is a typed object")
	require.Equal(t, "required", got[".seenBy{}.when"].(map[string]any)["presence"], "when appears in every merged map value")
	require.Equal(t, []any{"string"}, got[".seenBy{}.when"].(map[string]any)["types"], "the merged value's field keeps its type")
}

func TestInferMapsThresholds(t *testing.T) {
	t.Run("instances gate: 8 collapses, 7 does not", func(t *testing.T) {
		// Each instance carries 8 globally-unique keys, so distinct and ratio always
		// pass; only the object-instance count crosses the boundary.
		eight := make([][]string, 8)
		seven := make([][]string, 7)
		for i := 0; i < 8; i++ {
			eight[i] = nKeysOffset(i, 8)
			if i < 7 {
				seven[i] = nKeysOffset(i, 8)
			}
		}
		require.Equal(t, []any{"map"}, mapTypeOf(mapCorpus("m", eight), ".m"), "8 instances collapses")
		require.Equal(t, []any{"object"}, mapTypeOf(mapCorpus("m", seven), ".m"), "7 instances does not")
	})

	t.Run("distinct-keys gate: 8 collapses, 7 does not", func(t *testing.T) {
		// One key per instance; ratio 1.0 and objCount>=8 always pass, only the
		// distinct-key count crosses the boundary.
		eight := singleKeyInstances(nKeys(8))                           // objCount 8, distinct 8
		seven := append(singleKeyInstances(nKeys(7)), []string{"key0"}) // objCount 8, distinct 7
		require.Equal(t, []any{"map"}, mapTypeOf(mapCorpus("m", eight), ".m"), "8 distinct keys collapses")
		require.Equal(t, []any{"object"}, mapTypeOf(mapCorpus("m", seven), ".m"), "7 distinct keys does not")
	})

	t.Run("distinct ratio gate: 0.75 collapses, below does not", func(t *testing.T) {
		// 12 single-key instances (objCount 12, slots 12). 9 distinct -> ratio 0.75
		// collapses; 8 distinct -> ratio 0.667 does not (both clear the key gate).
		at := singleKeyInstances(nKeys(9))
		at = append(at, []string{"key0"}, []string{"key1"}, []string{"key2"}) // 12 instances, 9 distinct
		below := singleKeyInstances(nKeys(8))
		below = append(below, []string{"key0"}, []string{"key1"}, []string{"key2"}, []string{"key3"}) // 12 instances, 8 distinct
		require.Equal(t, []any{"map"}, mapTypeOf(mapCorpus("m", at), ".m"), "ratio 0.75 collapses")
		require.Equal(t, []any{"object"}, mapTypeOf(mapCorpus("m", below), ".m"), "ratio 0.667 does not")
	})

	t.Run("the root never collapses even when id-keyed", func(t *testing.T) {
		// Each item has one unique field name, so the root would look like a map;
		// collapsing it would hide the whole collection, so it must stay an object.
		items := map[string]any{}
		for i := 0; i < 12; i++ {
			items[strconv.Itoa(i)] = map[string]any{"field" + strconv.Itoa(i): 1}
		}
		got := shape.Infer(items).Comparable()
		require.Equal(t, []any{"object"}, got["$root"].(map[string]any)["types"], "root stays object")
		require.Contains(t, got, ".field0", "root keeps its literal child paths")
	})
}

// nKeysOffset returns n keys starting at offset*n, so different instances get
// globally-distinct keys.
func nKeysOffset(offset, n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = "key" + strconv.Itoa(offset*n+i)
	}
	return keys
}

// TestInferMapsNested collapses a doubly id-keyed corpus: recursion must descend
// into a collapsed map value and collapse the inner id-keyed map too.
func TestInferMapsNested(t *testing.T) {
	items := map[string]any{}
	for i := 0; i < 8; i++ {
		s := strconv.Itoa(i)
		items[s] = map[string]any{"f": map[string]any{
			"outer" + s: map[string]any{"inner" + s: map[string]any{"leaf": 1}},
		}}
	}
	got := shape.Infer(items).Comparable()
	require.Equal(t, []any{"map"}, got[".f"].(map[string]any)["types"], "the outer id-keyed object collapses")
	require.Equal(t, []any{"map"}, got[".f{}"].(map[string]any)["types"], "recursion collapses the inner id-keyed value too")
	require.Contains(t, got, ".f{}{}", "the twice-collapsed value shape is projected")
}

// TestInferMapsArrayElement collapses an id-keyed object that appears as an array
// element: recursion must descend through elem and collapse the element.
func TestInferMapsArrayElement(t *testing.T) {
	elems := make([]any, 8)
	for i := 0; i < 8; i++ {
		elems[i] = map[string]any{"u" + strconv.Itoa(i): map[string]any{"leaf": 1}}
	}
	got := shape.Infer(map[string]any{"1": map[string]any{"arr": elems}}).Comparable()
	require.Equal(t, []any{"map"}, got[".arr[]"].(map[string]any)["types"], "the id-keyed array element collapses to a map")
	require.Contains(t, got, ".arr[]{}", "the collapsed element's value shape is projected")
}

// TestInferMapsSlotsBoundary pins shouldCollapse's slots seed at 0: a corpus whose
// real distinct/slots ratio is just under 0.75 stays an object; a slots seed of -1
// would read the ratio as exactly 0.75 and wrongly collapse.
func TestInferMapsSlotsBoundary(t *testing.T) {
	// 9 single-key instances then 4 repeats: 13 instances, 9 distinct, 13 slots ->
	// ratio 9/13 = 0.692 (object). A -1 seed reads 9/12 = 0.75 and collapses.
	inst := singleKeyInstances(nKeys(9))
	inst = append(inst, []string{"key0"}, []string{"key1"}, []string{"key2"}, []string{"key3"})
	require.Equal(t, []any{"object"}, mapTypeOf(mapCorpus("m", inst), ".m"))
}

// TestInferMapsFormatMerge folds string formats across a map collapse: a format
// held by every id-keyed value survives; a format any value breaks falls away.
func TestInferMapsFormatMerge(t *testing.T) {
	// mapValues makes a corpus where field m is a one-key object per instance (so it
	// collapses) holding the i-th value; the id-keyed values fold together through
	// formats.mergeFrom.
	mapValues := func(values []string) map[string]any {
		items := make(map[string]any, len(values))
		for i, v := range values {
			s := strconv.Itoa(i)
			items[s] = map[string]any{"m": map[string]any{"u" + s: v}}
		}
		return items
	}
	// allSame: every value carries the same format, so the merge must keep it.
	allSame := func(v string) []string {
		out := make([]string, 8)
		for i := range out {
			out[i] = v
		}
		return out
	}
	// oneCarrier: a single format-carrying value among many plain strings. The
	// intersection (an all-or-nothing &&) must drop the format regardless of merge
	// order, so a && folded into || is caught deterministically (|| keeps the lone
	// carrier's format); the order-dependent operand-drop mutants are left as
	// documented residue.
	oneCarrier := func(v string) []string {
		out := make([]string, 0, 41)
		out = append(out, v)
		for i := 0; i < 40; i++ {
			out = append(out, "plain"+strconv.Itoa(i))
		}
		return out
	}
	tests := []struct {
		name   string
		values []string
		want   []any
	}{
		{"every value shares the date format", allSame("2020-01-02"), []any{"string(date)"}},
		{"one date-time among plain strings drops the format", oneCarrier("2020-01-02T03:04:05Z"), []any{"string"}},
		{"one date among plain strings drops the format", oneCarrier("2020-01-02"), []any{"string"}},
		{"one uuid among plain strings drops the format", oneCarrier("12345678-1234-1234-1234-123456789abc"), []any{"string"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, mapTypeOf(mapValues(tt.values), ".m{}"))
		})
	}
}

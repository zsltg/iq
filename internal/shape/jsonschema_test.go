package shape_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/shape"
)

// fieldSchema infers a single field "f" holding each of vals (one item per value)
// and returns the JSON Schema for that property.
func fieldSchema(vals []any) map[string]any {
	items := make(map[string]any, len(vals))
	for i, v := range vals {
		items[strconv.Itoa(i)] = map[string]any{"f": v}
	}
	doc := shape.Infer(items).JSONSchema("t")
	return doc["properties"].(map[string]any)["f"].(map[string]any)
}

// vals expands value->count into a flat slice of any-typed string values.
func vals(spec map[string]int) []any {
	var out []any
	for v, n := range spec {
		for range n {
			out = append(out, v)
		}
	}
	return out
}

// evenVals builds distinct string values, each repeated the same number of times.
func evenVals(distinct, each int) []any {
	spec := make(map[string]int, distinct)
	for i := range distinct {
		spec["v"+strconv.Itoa(i)] = each
	}
	return vals(spec)
}

func TestJSONSchemaEnumThresholds(t *testing.T) {
	tests := []struct {
		name     string
		vals     []any
		wantEnum []any // nil means: no enum key expected
	}{
		{"16 instances qualifies", evenVals(2, 8), []any{"v0", "v1"}},                                   // count 16, distinct 2, ratio 0.125
		{"15 instances too few", vals(map[string]int{"a": 8, "b": 7}), nil},                             // count 15
		{"8 distinct qualifies", evenVals(8, 5), []any{"v0", "v1", "v2", "v3", "v4", "v5", "v6", "v7"}}, // count 40, ratio 0.2
		{"9 distinct too many", evenVals(9, 5), nil},                                                    // distinct 9 over the ceiling
		{"ratio 0.25 qualifies", evenVals(4, 4), []any{"v0", "v1", "v2", "v3"}},                         // count 16, ratio 0.25
		{"ratio above 0.25 drops", vals(map[string]int{"a": 4, "b": 3, "c": 3, "d": 3, "e": 3}), nil},   // count 16, distinct 5, ratio 0.3125
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fieldSchema(tt.vals)
			require.Equal(t, "string", got["type"])
			if tt.wantEnum == nil {
				require.NotContains(t, got, "enum")
				return
			}
			require.Equal(t, tt.wantEnum, got["enum"])
		})
	}
}

func TestJSONSchemaEnumNeedsAllStrings(t *testing.T) {
	// 16 low-cardinality strings would qualify, but one integer value breaks the
	// all-string gate, so no enum — and the type is a mixed array.
	got := fieldSchema(append(vals(map[string]int{"a": 8, "b": 7}), 1))
	require.Equal(t, []any{"integer", "string"}, got["type"])
	require.NotContains(t, got, "enum")
}

func TestJSONSchemaScalarShapes(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want map[string]any
	}{
		{"integer", 5, map[string]any{"type": "integer"}},
		{"number", 5.5, map[string]any{"type": "number"}},
		{"boolean uses the json schema name", true, map[string]any{"type": "boolean"}},
		{"null", nil, map[string]any{"type": "null"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, fieldSchema([]any{tt.val}))
		})
	}
}

func TestJSONSchemaFormatAndArray(t *testing.T) {
	t.Run("date-time format", func(t *testing.T) {
		got := fieldSchema([]any{"2020-01-02T03:04:05Z", "2021-06-07T08:09:10Z"})
		require.Equal(t, map[string]any{"type": "string", "format": "date-time"}, got)
	})

	t.Run("array items", func(t *testing.T) {
		got := shape.Infer(map[string]any{"1": map[string]any{"a": []any{1, 2}}}).JSONSchema("t")
		a := got["properties"].(map[string]any)["a"].(map[string]any)
		require.Equal(t, map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, a)
	})

	t.Run("empty array has no items", func(t *testing.T) {
		got := shape.Infer(map[string]any{"1": map[string]any{"a": []any{}}}).JSONSchema("t")
		a := got["properties"].(map[string]any)["a"].(map[string]any)
		require.Equal(t, map[string]any{"type": "array"}, a)
	})
}

func TestJSONSchemaRoots(t *testing.T) {
	t.Run("non-object root is legal", func(t *testing.T) {
		got := shape.Infer(map[string]any{"1": "a", "2": "b"}).JSONSchema("keyspace")
		require.Equal(t, "https://json-schema.org/draft/2020-12/schema", got["$schema"])
		require.Equal(t, "keyspace", got["title"])
		require.Equal(t, "string", got["type"])
		require.NotContains(t, got, "properties")
	})

	t.Run("mixed root uses a type array", func(t *testing.T) {
		got := shape.Infer(map[string]any{"1": "a", "2": 1}).JSONSchema("t")
		require.Equal(t, []any{"integer", "string"}, got["type"])
	})
}

// TestJSONSchemaDocumentGolden pins the whole document for a shape that exercises
// required arrays, an enum, and a collapsed map together.
func TestJSONSchemaDocumentGolden(t *testing.T) {
	items := map[string]any{}
	for i := range 16 {
		items[strconv.Itoa(i)] = map[string]any{
			"status": []string{"active", "inactive"}[i%2],      // 16 instances, 2 distinct -> enum
			"ref":    map[string]any{"u" + strconv.Itoa(i): 1}, // 16 unique keys -> map of integer
		}
	}
	got := shape.Infer(items).JSONSchema("shop")
	require.Equal(t, map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title":   "shop",
		"type":    "object",
		"properties": map[string]any{
			"status": map[string]any{"type": "string", "enum": []any{"active", "inactive"}},
			"ref":    map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer"}},
		},
		"required": []any{"ref", "status"},
	}, got)
}

// TestJSONSchemaObjectRequired pins the object branch's field and required
// handling, which the gate flips, widens, and de-sorts.
func TestJSONSchemaObjectRequired(t *testing.T) {
	t.Run("an empty object has no properties key", func(t *testing.T) {
		require.Equal(t, map[string]any{"type": "object"}, fieldSchema([]any{map[string]any{}}))
	})

	t.Run("an all-optional object has no required key", func(t *testing.T) {
		got := shape.Infer(map[string]any{
			"1": map[string]any{"a": 1},
			"2": map[string]any{"b": 2},
		}).JSONSchema("t")
		require.Contains(t, got, "properties")
		require.NotContains(t, got, "required")
	})

	t.Run("exactly one required field lists just that field", func(t *testing.T) {
		got := shape.Infer(map[string]any{
			"1": map[string]any{"a": 1, "b": 2},
			"2": map[string]any{"a": 1},
		}).JSONSchema("t")
		require.Equal(t, []any{"a"}, got["required"])
	})

	t.Run("required fields are sorted", func(t *testing.T) {
		got := shape.Infer(map[string]any{
			"1": map[string]any{"e": 1, "d": 1, "c": 1, "b": 1, "a": 1, "f": 1},
		}).JSONSchema("t")
		require.Equal(t, []any{"a", "b", "c", "d", "e", "f"}, got["required"])
	})
}

// TestJSONSchemaEnumInMap projects an enum through a map collapse: the id-keyed
// values fold into the map value's enum accumulator, so a low-cardinality set
// still surfaces as additionalProperties.enum with its full sorted value set.
func TestJSONSchemaEnumInMap(t *testing.T) {
	items := map[string]any{}
	for i := range 16 {
		s := strconv.Itoa(i)
		items[s] = map[string]any{"m": map[string]any{"u" + s: []string{"active", "inactive"}[i%2]}}
	}
	got := shape.Infer(items).JSONSchema("t")
	ap := got["properties"].(map[string]any)["m"].(map[string]any)["additionalProperties"].(map[string]any)
	require.Equal(t, "string", ap["type"])
	require.Equal(t, []any{"active", "inactive"}, ap["enum"])
}

// mapEnumSchema infers field m as a one-key object per instance, so m collapses
// to a map and every value folds into the map value through enumAcc.mergeFrom.
// Instance i holds keys[i] with the string values[i]. It returns the
// additionalProperties schema of m.
func mapEnumSchema(keys, values []string) map[string]any {
	items := make(map[string]any, len(keys))
	for i := range keys {
		items[strconv.Itoa(i)] = map[string]any{"m": map[string]any{keys[i]: values[i]}}
	}
	got := shape.Infer(items).JSONSchema("t")
	return got["properties"].(map[string]any)["m"].(map[string]any)["additionalProperties"].(map[string]any)
}

// cycleVals returns n strings that cycle through v0..v(distinct-1).
func cycleVals(distinct, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "v" + strconv.Itoa(i%distinct)
	}
	return out
}

// uniqueKeys returns the n keys u0..u(n-1).
func uniqueKeys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "u" + strconv.Itoa(i)
	}
	return out
}

// TestJSONSchemaEnumMergeInMap pins the distinct ceiling of an enum that a map
// collapse builds. The merge must keep the over state of a map value that saw
// too many distinct strings. It must also set over when the union of the values
// goes past the ceiling, although no single value did.
func TestJSONSchemaEnumMergeInMap(t *testing.T) {
	// Key "hot" is in nine instances with nine distinct strings, so its own enum
	// is over. 23 unique keys follow, each with the string "x". The key ratio is
	// 24 distinct keys over 32 slots, which is 0.75, so m collapses.
	hotKeys := make([]string, 0, 32)
	hotVals := make([]string, 0, 32)
	for i := range 9 {
		hotKeys = append(hotKeys, "hot")
		hotVals = append(hotVals, "h"+strconv.Itoa(i))
	}
	for i := range 23 {
		hotKeys = append(hotKeys, "k"+strconv.Itoa(i))
		hotVals = append(hotVals, "x")
	}

	tests := []struct {
		name     string
		keys     []string
		values   []string
		wantEnum []any // nil means: no enum key expected
	}{
		{"a value that is over keeps the merged enum over", hotKeys, hotVals, nil},
		{"nine distinct strings across the values are over", uniqueKeys(36), cycleVals(9, 36), nil},
		{"eight distinct strings across the values qualify", uniqueKeys(32), cycleVals(8, 32), []any{"v0", "v1", "v2", "v3", "v4", "v5", "v6", "v7"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Go randomizes the merge order, so the corpus is inferred ten times.
			for pass := range 10 {
				got := mapEnumSchema(tt.keys, tt.values)
				require.Equal(t, "string", got["type"], "pass %d", pass)
				if tt.wantEnum == nil {
					require.NotContains(t, got, "enum", "pass %d", pass)
					continue
				}
				require.Equal(t, tt.wantEnum, got["enum"], "pass %d", pass)
			}
		})
	}
}

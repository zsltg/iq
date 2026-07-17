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
		for i := 0; i < n; i++ {
			out = append(out, v)
		}
	}
	return out
}

// evenVals builds distinct string values, each repeated the same number of times.
func evenVals(distinct, each int) []any {
	spec := make(map[string]int, distinct)
	for i := 0; i < distinct; i++ {
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
		require.Equal(t, "http://json-schema.org/draft-07/schema#", got["$schema"])
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
	for i := 0; i < 16; i++ {
		items[strconv.Itoa(i)] = map[string]any{
			"status": []string{"active", "inactive"}[i%2],      // 16 instances, 2 distinct -> enum
			"ref":    map[string]any{"u" + strconv.Itoa(i): 1}, // 16 unique keys -> map of integer
		}
	}
	got := shape.Infer(items).JSONSchema("shop")
	require.Equal(t, map[string]any{
		"$schema": "http://json-schema.org/draft-07/schema#",
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
	for i := 0; i < 16; i++ {
		s := strconv.Itoa(i)
		items[s] = map[string]any{"m": map[string]any{"u" + s: []string{"active", "inactive"}[i%2]}}
	}
	got := shape.Infer(items).JSONSchema("t")
	ap := got["properties"].(map[string]any)["m"].(map[string]any)["additionalProperties"].(map[string]any)
	require.Equal(t, "string", ap["type"])
	require.Equal(t, []any{"active", "inactive"}, ap["enum"])
}

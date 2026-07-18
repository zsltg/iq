package shape_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/shape"
)

// odcsField infers a single field "f" holding each of vals (one item per value)
// and returns its ODCS property element from the projected schema object.
func odcsField(vals ...any) map[string]any {
	items := make(map[string]any, len(vals))
	for i, v := range vals {
		items[strconv.Itoa(i)] = map[string]any{"f": v}
	}
	obj := shape.Infer(items).ODCSSchemaObject("t")
	for _, p := range obj["properties"].([]any) {
		prop := p.(map[string]any)
		if prop["name"] == "f" {
			return prop
		}
	}
	return nil
}

// TestODCSScalarTypes pins the scalar shape->ODCS logicalType mapping, including
// the format demotions ODCS's nine-type lattice forces: date-time and date carry
// logicalType date with a JDK format pattern, a UUID stays string with the uuid
// format, and iq's canonical decimal-string and base64-binary values (plain
// strings in the shape) stay logicalType string with no invented decimal/binary
// type or non-enum format.
func TestODCSScalarTypes(t *testing.T) {
	tests := []struct {
		name       string
		val        any
		wantType   string
		wantFormat any // nil = no logicalTypeOptions expected
	}{
		{"string", "hello", "string", nil},
		{"integer", 42, "integer", nil},
		{"number", 3.14, "number", nil},
		{"boolean", true, "boolean", nil},
		{"date-time demotes to date", "2020-01-02T03:04:05Z", "date", "yyyy-MM-dd'T'HH:mm:ssXXX"},
		{"date demotes to date", "2020-01-02", "date", "yyyy-MM-dd"},
		{"uuid stays string with format", "123e4567-e89b-12d3-a456-426614174000", "string", "uuid"},
		{"decimal string stays plain string", "123.45", "string", nil},
		{"base64 binary stays plain string", "aGVsbG8gd29ybGQ=", "string", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop := odcsField(tt.val)
			require.Equal(t, tt.wantType, prop["logicalType"])
			if tt.wantFormat == nil {
				require.NotContains(t, prop, "logicalTypeOptions")
				return
			}
			opts := prop["logicalTypeOptions"].(map[string]any)
			require.Equal(t, tt.wantFormat, opts["format"])
		})
	}
}

// TestODCSNumberIntegerCollapse pins that a field seen as both integer and number
// projects the widened logicalType number, mirroring the JSON Schema projection.
func TestODCSNumberIntegerCollapse(t *testing.T) {
	prop := odcsField(1, 2.5)
	require.Equal(t, "number", prop["logicalType"])
}

// TestODCSNestedObject pins that a nested object projects logicalType object with
// a properties array, and that presence maps to the per-property required flag:
// a field in every instance is required, an optional one omits the flag.
func TestODCSNestedObject(t *testing.T) {
	items := map[string]any{
		"1": map[string]any{"addr": map[string]any{"city": "A", "zip": "1"}},
		"2": map[string]any{"addr": map[string]any{"city": "B"}},
	}
	obj := shape.Infer(items).ODCSSchemaObject("t")
	require.Equal(t, "object", obj["logicalType"])

	var addr map[string]any
	for _, p := range obj["properties"].([]any) {
		if prop := p.(map[string]any); prop["name"] == "addr" {
			addr = prop
		}
	}
	require.NotNil(t, addr)
	require.Equal(t, "object", addr["logicalType"])
	require.True(t, addr["required"].(bool), "addr is in every instance")

	props := addr["properties"].([]any)
	// Deterministic order: sorted by name, city before zip.
	city := props[0].(map[string]any)
	zip := props[1].(map[string]any)
	require.Equal(t, "city", city["name"])
	require.True(t, city["required"].(bool), "city is in every instance")
	require.Equal(t, "zip", zip["name"])
	require.NotContains(t, zip, "required", "zip is optional (present in one of two)")
}

// TestODCSArray pins that an array field projects logicalType array with an items
// element carrying the element's logicalType, and that the item element is
// nameless (ODCS array items are unnamed element definitions).
func TestODCSArray(t *testing.T) {
	t.Run("scalar elements", func(t *testing.T) {
		prop := odcsField([]any{1, 2, 3})
		require.Equal(t, "array", prop["logicalType"])
		items := prop["items"].(map[string]any)
		require.Equal(t, "integer", items["logicalType"])
		require.NotContains(t, items, "name")
	})
	t.Run("object elements nest properties", func(t *testing.T) {
		prop := odcsField([]any{map[string]any{"k": "v"}})
		items := prop["items"].(map[string]any)
		require.Equal(t, "object", items["logicalType"])
		inner := items["properties"].([]any)[0].(map[string]any)
		require.Equal(t, "k", inner["name"])
		require.Equal(t, "string", inner["logicalType"])
	})
}

// TestODCSMapValue pins the map projection directly: a nested id-keyed object
// collapsed into a map is a dynamic-keyed object with no properties array — ODCS
// v3.1.0 has no additionalProperties analog, so the value shape is not named. The
// root holds field "m" whose distinct per-instance keys make it collapse.
func TestODCSMapValue(t *testing.T) {
	items := make(map[string]any, 12)
	for i := 0; i < 12; i++ {
		items[strconv.Itoa(i)] = map[string]any{
			"m": map[string]any{"k" + strconv.Itoa(i): map[string]any{"v": i}},
		}
	}
	obj := shape.Infer(items).ODCSSchemaObject("t")
	var m map[string]any
	for _, p := range obj["properties"].([]any) {
		if prop := p.(map[string]any); prop["name"] == "m" {
			m = prop
		}
	}
	require.NotNil(t, m, "field m must be present")
	require.Equal(t, "object", m["logicalType"])
	require.NotContains(t, m, "properties", "an id-keyed map has no named properties")
}

// TestODCSNonObjectRoot pins that a non-object keyspace is legal: a string
// keyspace projects a schema object with logicalType string and no properties.
func TestODCSNonObjectRoot(t *testing.T) {
	obj := shape.Infer(map[string]any{"1": "plain", "2": "text"}).ODCSSchemaObject("kv")
	require.Equal(t, "kv", obj["name"])
	require.Equal(t, "string", obj["logicalType"])
	require.NotContains(t, obj, "properties")
}

// TestODCSDeterministic pins that two independent samples of the same shape
// project byte-identical ODCS schema objects — property order is sorted, so the
// contract is reproducible under resampling.
func TestODCSDeterministic(t *testing.T) {
	build := func(seed int) map[string]any {
		items := make(map[string]any, 5)
		for i := 0; i < 5; i++ {
			items[strconv.Itoa(seed*100+i)] = map[string]any{"b": 2, "a": "x", "c": true}
		}
		return shape.Infer(items).ODCSSchemaObject("t")
	}
	require.Equal(t, build(1), build(2))

	// Property order is sorted by name regardless of insertion order.
	props := build(1)["properties"].([]any)
	names := make([]string, len(props))
	for i, p := range props {
		names[i] = p.(map[string]any)["name"].(string)
	}
	require.Equal(t, []string{"a", "b", "c"}, names)
}

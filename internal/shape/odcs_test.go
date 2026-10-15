package shape_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/shape"
)

// odcsObject infers the shape of items and returns the single ODCS schema object
// of its contract, named "t".
func odcsObject(t *testing.T, items map[string]any) map[string]any {
	t.Helper()
	contract, err := shape.Infer(items).ODCSContract("t")
	require.NoError(t, err)
	return contract["schema"].([]any)[0].(map[string]any)
}

// odcsProp returns the property element called name from an ODCS element.
func odcsProp(t *testing.T, el map[string]any, name string) map[string]any {
	t.Helper()
	for _, p := range el["properties"].([]any) {
		if prop := p.(map[string]any); prop["name"] == name {
			return prop
		}
	}
	require.Failf(t, "property missing", "no property %q", name)
	return nil
}

// odcsField infers a single field "f" holding each of vals (one item per value)
// and returns its ODCS property element from the projected schema object.
func odcsField(t *testing.T, vals ...any) map[string]any {
	t.Helper()
	return odcsProp(t, odcsObject(t, fieldItems(vals...)), "f")
}

// idMapItems builds 12 items whose field "m" holds one unique key each, so the
// field collapses into a map. The map value is val.
func idMapItems(val func(i int) any) map[string]any {
	items := make(map[string]any, 12)
	for i := range 12 {
		items[strconv.Itoa(i)] = map[string]any{"m": map[string]any{"k" + strconv.Itoa(i): val(i)}}
	}
	return items
}

// TestODCSScalarTypes pins the scalar shape->ODCS logicalType mapping, including
// the format demotions that the ODCS type lattice forces. A date-time becomes
// logicalType timestamp with no option, because a JDK pattern cannot express a
// variable-width fraction. A date carries logicalType date with a JDK format
// pattern. A UUID stays string with the uuid format. The canonical decimal-string and base64-binary values of iq (plain
// strings in the shape) stay logicalType string. They get no invented
// decimal/binary type and no non-enum format.
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
		{"date-time whole seconds is timestamp", "2020-01-02T03:04:05Z", "timestamp", nil},
		{"date-time fractional seconds is timestamp", "2020-01-02T03:04:05.123456Z", "timestamp", nil},
		{"date-time with offset is timestamp", "2020-01-02T03:04:05+02:00", "timestamp", nil},
		{"date stays date", "2020-01-02", "date", "yyyy-MM-dd"},
		{"uuid stays string with format", "123e4567-e89b-12d3-a456-426614174000", "string", "uuid"},
		{"decimal string stays plain string", "123.45", "string", nil},
		{"base64 binary stays plain string", "aGVsbG8gd29ybGQ=", "string", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop := odcsField(t, tt.val)
			require.Equal(t, tt.wantType, prop["logicalType"])
			// A scalar is neither an object nor an array: it carries no
			// properties and no items slot.
			require.NotContains(t, prop, "properties")
			require.NotContains(t, prop, "items")
			if tt.wantFormat == nil {
				require.NotContains(t, prop, "logicalTypeOptions")
				return
			}
			opts := prop["logicalTypeOptions"].(map[string]any)
			require.Equal(t, tt.wantFormat, opts["format"])
		})
	}
}

// TestODCSEmptyObject pins the object-boundary guard. An empty object carries
// logicalType object but no properties key. The properties slot is emitted only
// for a strictly non-empty field set, so an empty object never yields an empty
// properties array.
func TestODCSEmptyObject(t *testing.T) {
	prop := odcsField(t, map[string]any{})
	require.Equal(t, "object", prop["logicalType"])
	require.NotContains(t, prop, "properties", "an empty object has no properties slot")
}

// TestODCSEmptyArray pins the array-boundary guard. An empty array carries
// logicalType array but no items key. The items key is emitted only when the array
// has a unified element, which an empty array never accumulates.
func TestODCSEmptyArray(t *testing.T) {
	prop := odcsField(t, []any{})
	require.Equal(t, "array", prop["logicalType"])
	require.NotContains(t, prop, "items", "an empty array has no items slot")
}

// TestODCSNumberIntegerCollapse pins that a field seen as both integer and number
// projects the widened logicalType number, mirroring the JSON Schema projection.
func TestODCSNumberIntegerCollapse(t *testing.T) {
	prop := odcsField(t, 1, 2.5)
	require.Equal(t, "number", prop["logicalType"])
}

// TestODCSNestedObject pins that a nested object projects logicalType object with
// a properties array. It also pins that presence maps to the per-property required
// flag. A field in every instance is required. An optional one omits the flag.
func TestODCSNestedObject(t *testing.T) {
	items := map[string]any{
		"1": map[string]any{"addr": map[string]any{"city": "A", "zip": "1"}},
		"2": map[string]any{"addr": map[string]any{"city": "B"}},
	}
	obj := odcsObject(t, items)
	require.Equal(t, "object", obj["logicalType"])

	addr := odcsProp(t, obj, "addr")
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
// element carrying the element's logicalType. It also pins that the item element
// is nameless (ODCS array items are unnamed element definitions).
func TestODCSArray(t *testing.T) {
	t.Run("scalar elements", func(t *testing.T) {
		prop := odcsField(t, []any{1, 2, 3})
		require.Equal(t, "array", prop["logicalType"])
		items := prop["items"].(map[string]any)
		require.Equal(t, "integer", items["logicalType"])
		require.NotContains(t, items, "name")
	})
	t.Run("object elements nest properties", func(t *testing.T) {
		prop := odcsField(t, []any{map[string]any{"k": "v"}})
		items := prop["items"].(map[string]any)
		require.Equal(t, "object", items["logicalType"])
		inner := items["properties"].([]any)[0].(map[string]any)
		require.Equal(t, "k", inner["name"])
		require.Equal(t, "string", inner["logicalType"])
	})
}

// TestODCSMap pins the map projection. An id-keyed object collapses into a map.
// It renders logicalType map with a string key and the value element, and it has
// no properties slot. A nested map inside the value renders the same way. The root
// holds field "m", whose distinct per-instance keys make it collapse.
func TestODCSMap(t *testing.T) {
	t.Run("object value", func(t *testing.T) {
		m := odcsProp(t, odcsObject(t, idMapItems(func(i int) any { return map[string]any{"v": i} })), "m")
		require.Equal(t, "map", m["logicalType"])
		require.NotContains(t, m, "properties", "a map has no named properties")
		require.NotContains(t, m, "items")
		mp := m["map"].(map[string]any)
		require.Equal(t, map[string]any{"logicalType": "string"}, mp["key"])
		value := mp["value"].(map[string]any)
		require.Equal(t, "object", value["logicalType"])
		require.NotContains(t, value, "name", "the map value element is nameless")
		inner := value["properties"].([]any)[0].(map[string]any)
		require.Equal(t, "v", inner["name"])
		require.Equal(t, "integer", inner["logicalType"])
	})
	t.Run("nested map in the value", func(t *testing.T) {
		items := idMapItems(func(i int) any {
			inner := make(map[string]any, 1)
			inner["x"+strconv.Itoa(i)] = i
			return map[string]any{"inner": inner}
		})
		m := odcsProp(t, odcsObject(t, items), "m")
		require.Equal(t, "map", m["logicalType"])
		value := m["map"].(map[string]any)["value"].(map[string]any)
		require.Equal(t, "object", value["logicalType"])
		in := odcsProp(t, value, "inner")
		require.Equal(t, "map", in["logicalType"])
		inner := in["map"].(map[string]any)
		require.Equal(t, map[string]any{"logicalType": "string"}, inner["key"])
		require.Equal(t, "integer", inner["value"].(map[string]any)["logicalType"])
	})
	t.Run("map plus array emits only the map slot", func(t *testing.T) {
		items := idMapItems(func(i int) any { return i })
		items["arr"] = map[string]any{"m": []any{1}}
		m := odcsProp(t, odcsObject(t, items), "m")
		require.Equal(t, "map", m["logicalType"])
		require.Contains(t, m, "map")
		require.NotContains(t, m, "items")
		require.NotContains(t, m, "properties")
	})
	t.Run("map value mixing object and array emits one slot", func(t *testing.T) {
		items := idMapItems(func(i int) any {
			if i%2 == 0 {
				return map[string]any{"v": i}
			}
			return []any{i}
		})
		value := odcsProp(t, odcsObject(t, items), "m")["map"].(map[string]any)["value"].(map[string]any)
		require.Equal(t, "object", value["logicalType"])
		require.Contains(t, value, "properties")
		require.NotContains(t, value, "items")
	})
}

// TestODCSMixedComposites pins that a node of mixed composite kinds picks one
// logical type and emits only the slot of that type. The type priority is object,
// then array.
func TestODCSMixedComposites(t *testing.T) {
	prop := odcsField(t, map[string]any{"a": 1}, []any{1})
	require.Equal(t, "object", prop["logicalType"])
	require.Contains(t, prop, "properties")
	require.NotContains(t, prop, "items")
	require.NotContains(t, prop, "map")
}

// TestODCSRequired pins that required means never null. A field is required only
// when every object holds it and it never held null.
func TestODCSRequired(t *testing.T) {
	tests := []struct {
		name         string
		items        map[string]any
		wantRequired bool
	}{
		{"always present and non-null", map[string]any{"1": map[string]any{"f": "a"}, "2": map[string]any{"f": "b"}}, true},
		{"null only", map[string]any{"1": map[string]any{"f": nil}, "2": map[string]any{"f": nil}}, false},
		{"string plus null", map[string]any{"1": map[string]any{"f": "a"}, "2": map[string]any{"f": nil}}, false},
		{"missing in some objects", map[string]any{"1": map[string]any{"f": "a"}, "2": map[string]any{"g": 1}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop := odcsProp(t, odcsObject(t, tt.items), "f")
			if tt.wantRequired {
				require.Equal(t, true, prop["required"])
				return
			}
			require.NotContains(t, prop, "required")
		})
	}
}

// TestODCSEnum pins that a string-only node with an inferred enum renders enum
// objects in sorted order, like JSON Schema. A node that also holds null, or that
// mixes kinds, gets no enum. So does a string node that misses the thresholds.
func TestODCSEnum(t *testing.T) {
	repeat := func(extra ...any) []any {
		vals := make([]any, 0, 20)
		for i := range 18 {
			vals = append(vals, []string{"b", "a", "c"}[i%3])
		}
		return append(vals, extra...)
	}
	tests := []struct {
		name     string
		vals     []any
		wantEnum []any
	}{
		{"string only", repeat("a", "a"), []any{
			map[string]any{"value": "a"}, map[string]any{"value": "b"}, map[string]any{"value": "c"},
		}},
		{"string plus null", repeat(nil, nil), nil},
		{"mixed kinds", repeat(1, 2), nil},
		{"too few values", []any{"a", "b"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prop := odcsField(t, tt.vals...)
			if tt.wantEnum == nil {
				require.NotContains(t, prop, "enum")
				return
			}
			require.Equal(t, tt.wantEnum, prop["enum"])
		})
	}
}

// TestODCSRootRejected pins that a root holding any kind other than object and
// null has no valid contract and returns the sentinel. A root of objects and nulls
// passes.
func TestODCSRootRejected(t *testing.T) {
	obj := map[string]any{"a": 1}
	tests := []struct {
		name    string
		items   map[string]any
		wantErr string
	}{
		{"string root", map[string]any{"1": "x", "2": "y"}, "the root holds string values"},
		{"array root", map[string]any{"1": []any{1}}, "the root holds array values"},
		{"number root", map[string]any{"1": 1.5}, "the root holds number values"},
		{"integer root", map[string]any{"1": 1}, "the root holds integer values"},
		{"boolean root", map[string]any{"1": true}, "the root holds bool values"},
		{"object plus string root", map[string]any{"1": obj, "2": "x"}, "the root holds string values"},
		{"several kinds sort by name", map[string]any{"1": obj, "2": "x", "3": []any{1}}, "the root holds array and string values"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract, err := shape.Infer(tt.items).ODCSContract("t")
			require.ErrorIs(t, err, shape.ErrODCSNotRecords)
			require.ErrorContains(t, err, tt.wantErr)
			require.Nil(t, contract)
		})
	}
	t.Run("object plus null root passes", func(t *testing.T) {
		obj := odcsObject(t, map[string]any{"1": obj, "2": nil})
		require.Equal(t, "object", obj["logicalType"])
	})
}

// TestODCSContractEnvelope pins the contract envelope, including the apiVersion.
func TestODCSContractEnvelope(t *testing.T) {
	contract, err := shape.Infer(map[string]any{"1": map[string]any{"a": 1}}).ODCSContract("shop.orders")
	require.NoError(t, err)
	require.Equal(t, "v3.2.0", contract["apiVersion"])
	require.Equal(t, "DataContract", contract["kind"])
	require.Equal(t, "shop.orders", contract["id"])
	require.Equal(t, "shop.orders", contract["name"])
	require.Equal(t, "1.0.0", contract["version"])
	require.Equal(t, "draft", contract["status"])
	schema := contract["schema"].([]any)
	require.Len(t, schema, 1)
	require.Equal(t, "shop.orders", schema[0].(map[string]any)["name"])
}

// TestODCSDeterministic pins that two independent samples of the same shape
// project byte-identical ODCS schema objects. Property order is sorted, so the
// contract is reproducible under resampling.
func TestODCSDeterministic(t *testing.T) {
	build := func(seed int) map[string]any {
		items := make(map[string]any, 5)
		for i := range 5 {
			items[strconv.Itoa(seed*100+i)] = map[string]any{"b": 2, "a": "x", "c": true}
		}
		return odcsObject(t, items)
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

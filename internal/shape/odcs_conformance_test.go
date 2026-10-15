package shape_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/shape"
)

// compileODCSSchema compiles the vendored official ODCS v3.2.0 JSON Schema.
func compileODCSSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile("testdata/odcs-json-schema-v3.2.0.json")
	require.NoError(t, err)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	c := jsonschema.NewCompiler()
	require.NoError(t, c.AddResource("odcs.json", doc))
	sch, err := c.Compile("odcs.json")
	require.NoError(t, err)
	return sch
}

// TestODCSConformance validates the full contract of each fixture against the
// official ODCS v3.2.0 JSON Schema. Every row must validate.
func TestODCSConformance(t *testing.T) {
	sch := compileODCSSchema(t)
	obj := map[string]any{"a": 1}
	tests := []struct {
		name  string
		items map[string]any
	}{
		{"map", idMapItems(func(i int) any { return map[string]any{"v": i} })},
		{"nested map", idMapItems(func(i int) any {
			return map[string]any{"inner": map[string]any{"x" + strconv.Itoa(i): i}}
		})},
		{"enum", enumItems()},
		{"timestamp", fieldItems("2020-01-02T03:04:05.123Z", "2020-01-02T03:04:05+02:00")},
		{"date", fieldItems("2020-01-02")},
		{"uuid", fieldItems("123e4567-e89b-12d3-a456-426614174000")},
		{"array of objects", fieldItems([]any{map[string]any{"k": "v"}})},
		{"nested object", fieldItems(map[string]any{"city": "A", "zip": "1"})},
		{"null-only field", fieldItems(nil)},
		{"object plus array field", fieldItems(map[string]any{"a": 1}, []any{1})},
		{"map plus array field", func() map[string]any {
			items := idMapItems(func(i int) any { return i })
			items["arr"] = map[string]any{"m": []any{1}}
			return items
		}()},
		{"object plus null root", map[string]any{"1": obj, "2": nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract, err := shape.Infer(tt.items).ODCSContract("t")
			require.NoError(t, err)
			// Round-trip through JSON so the validator sees plain JSON values.
			raw, err := json.Marshal(contract)
			require.NoError(t, err)
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			require.NoError(t, err)
			require.NoError(t, sch.Validate(inst))
		})
	}
}

// fieldItems builds one item per value, each holding the value in field "f".
func fieldItems(vals ...any) map[string]any {
	items := make(map[string]any, len(vals))
	for i, v := range vals {
		items[strconv.Itoa(i)] = map[string]any{"f": v}
	}
	return items
}

// enumItems builds 20 items whose field "f" takes three repeated string values,
// enough to infer an enum.
func enumItems() map[string]any {
	vals := make([]any, 20)
	for i := range vals {
		vals[i] = []string{"a", "b", "c"}[i%3]
	}
	return fieldItems(vals...)
}

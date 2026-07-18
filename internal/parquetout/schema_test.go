package parquetout

import (
	"bytes"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/stretchr/testify/require"
)

// field returns the named top-level column of the inferred schema, failing the
// test when it is absent.
func field(t *testing.T, s *arrow.Schema, name string) arrow.Field {
	t.Helper()
	fs, ok := s.FieldsByName(name)
	require.Truef(t, ok, "column %q missing from schema %s", name, s)
	require.Len(t, fs, 1)
	return fs[0]
}

// presenceOf returns a field's iq:presence metadata value.
func presenceOf(t *testing.T, f arrow.Field) string {
	t.Helper()
	v, ok := f.Metadata.GetValue(metaPresence)
	require.Truef(t, ok, "field %q has no %s metadata", f.Name, metaPresence)
	return v
}

// isJSONField reports whether a field is the arrow.json byte-lossless fallback:
// plain utf8 storage marked by the iq:extension metadata.
func isJSONField(f arrow.Field) bool {
	v, ok := f.Metadata.GetValue(metaExtName)
	return ok && v == jsonExtName && arrow.TypeEqual(arrow.BinaryTypes.String, f.Type)
}

func TestInferPlanScalarProjection(t *testing.T) {
	// Ten object rows exercise every scalar mapping row plus presence: "req" in
	// all ten, "opt" in half. "mix" sees an integer and a fractional number, so
	// integer∪number collapses to double.
	sample := make([]any, 0, 10)
	for i := 0; i < 10; i++ {
		row := map[string]any{
			"req":  i,
			"flt":  1.5,
			"mix":  3,
			"bln":  true,
			"str":  "x",
			"tsp":  "2021-01-02T03:04:05Z",
			"dat":  "2021-01-02",
			"uid":  "123e4567-e89b-12d3-a456-426614174000",
			"het":  i,
			"nul":  nil,
			"deep": map[string]any{"a": 1},
			"lst":  []any{1, 2, 3},
		}
		if i%2 == 0 {
			row["opt"] = i
		}
		if i == 0 {
			row["mix"] = 1 // integer
		}
		if i == 1 {
			row["mix"] = 2.5 // number: forces the integer∪number → double collapse
		}
		if i%2 == 1 {
			row["het"] = "s" // integer in some rows, string in others → heterogeneous
		}
		sample = append(sample, row)
	}

	p := inferPlan(sample)
	require.False(t, p.singleValue)
	s := p.schema

	cases := []struct {
		name     string
		dt       arrow.DataType
		presence string
		json     bool
	}{
		{"req", arrow.PrimitiveTypes.Int64, "required", false},
		{"flt", arrow.PrimitiveTypes.Float64, "required", false},
		{"mix", arrow.PrimitiveTypes.Float64, "required", false},
		{"bln", arrow.FixedWidthTypes.Boolean, "required", false},
		{"str", arrow.BinaryTypes.String, "required", false},
		{"tsp", &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"}, "required", false},
		{"dat", arrow.FixedWidthTypes.Date32, "required", false},
		{"uid", arrow.BinaryTypes.String, "required", false},
		{"het", nil, "required", true}, // present in every row, but integer∪string
		{"nul", nil, "required", true}, // always null → null-only
		{"opt", arrow.PrimitiveTypes.Int64, "optional", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := field(t, s, tc.name)
			require.True(t, f.Nullable)
			require.Equal(t, tc.presence, presenceOf(t, f))
			if tc.json {
				require.Truef(t, isJSONField(f), "column %q should be arrow.json, got %s", tc.name, f.Type)
				return
			}
			require.Falsef(t, isJSONField(f), "column %q should not be arrow.json", tc.name)
			require.Truef(t, arrow.TypeEqual(tc.dt, f.Type), "column %q: want %s got %s", tc.name, tc.dt, f.Type)
		})
	}
}

func TestInferPlanNestedProjection(t *testing.T) {
	sample := []any{
		map[string]any{"deep": map[string]any{"a": 1, "b": "y"}, "lst": []any{1, 2}},
		map[string]any{"deep": map[string]any{"a": 2, "b": "z"}, "lst": []any{3}},
	}
	p := inferPlan(sample)

	t.Run("object becomes struct", func(t *testing.T) {
		f := field(t, p.schema, "deep")
		st, ok := f.Type.(*arrow.StructType)
		require.True(t, ok, "deep should be a struct, got %s", f.Type)
		a, ok := st.FieldByName("a")
		require.True(t, ok)
		require.True(t, arrow.TypeEqual(arrow.PrimitiveTypes.Int64, a.Type))
		require.Equal(t, "required", presenceOf(t, a))
		b, ok := st.FieldByName("b")
		require.True(t, ok)
		require.True(t, arrow.TypeEqual(arrow.BinaryTypes.String, b.Type))
	})

	t.Run("array becomes list", func(t *testing.T) {
		f := field(t, p.schema, "lst")
		lt, ok := f.Type.(*arrow.ListType)
		require.True(t, ok, "lst should be a list, got %s", f.Type)
		require.True(t, arrow.TypeEqual(arrow.PrimitiveTypes.Int64, lt.Elem()))
	})
}

func TestInferPlanIDKeyedMap(t *testing.T) {
	// Eight object rows whose "cfg" object carries distinct keys each trip the
	// id-keyed-map heuristic (mapMinInstances, mapMinKeys, mapMinDistinctRatio).
	sample := make([]any, 0, 8)
	for i := 0; i < 8; i++ {
		sample = append(sample, map[string]any{
			"id": i,
			"cfg": map[string]any{
				kkey(i, "a"): 1,
				kkey(i, "b"): 2,
			},
		})
	}
	p := inferPlan(sample)

	f := field(t, p.schema, "cfg")
	mt, ok := f.Type.(*arrow.MapType)
	require.True(t, ok, "cfg should be a map, got %s", f.Type)
	require.True(t, arrow.TypeEqual(arrow.BinaryTypes.String, mt.KeyType()))
	require.True(t, arrow.TypeEqual(arrow.PrimitiveTypes.Int64, mt.ItemType()))
}

func TestInferPlanSingleValueRoot(t *testing.T) {
	// A non-object stream (scalars) yields one "value" column, not fields.
	p := inferPlan([]any{"a", "b", "c"})
	require.True(t, p.singleValue)
	require.Len(t, p.columns, 1)
	require.Equal(t, "value", p.columns[0].name)
	f := field(t, p.schema, "value")
	require.Equal(t, "value", f.Name)
	require.True(t, f.Nullable)
	require.Equal(t, "required", presenceOf(t, f))
	require.True(t, arrow.TypeEqual(arrow.BinaryTypes.String, f.Type))
}

// TestWriterSingleValueRoundTrip pins the single-"value"-column path end to end:
// a scalar stream reads back with its values under the "value" column.
func TestWriterSingleValueRoundTrip(t *testing.T) {
	tbl := readTable(t, writeParquet(t, []any{"a", "b", "c"}))
	require.Equal(t, int64(3), tbl.NumRows())
	rec := oneRecord(t, tbl)
	col := colOf(t, rec, "value").(*array.String)
	require.Equal(t, "a", col.Value(0))
	require.Equal(t, "b", col.Value(1))
	require.Equal(t, "c", col.Value(2))
}

// TestWriterSingleValuePostSampleError pins the single-value error path, which
// names the "value" column: a sample of valid timestamps locks the column, then
// a non-timestamp past the sample fails naming that column.
func TestWriterSingleValuePostSampleError(t *testing.T) {
	pw := NewWriter(&bytes.Buffer{})
	for i := 0; i < sampleBufferSize; i++ {
		require.NoError(t, pw.Add("2021-01-02T03:04:05Z"))
	}
	err := pw.Add("not-a-timestamp")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"value"`)
}

func TestInferPlanEmptyObjects(t *testing.T) {
	// Objects with no observed fields have no "properties" in the JSON Schema, so
	// objectType falls back to a single arrow.json "value" column.
	p := inferPlan([]any{map[string]any{}, map[string]any{}, map[string]any{}})
	require.True(t, p.singleValue)
	f := field(t, p.schema, "value")
	require.True(t, isJSONField(f), "an all-empty-object stream should yield an arrow.json value column")
}

func TestInferPlanEmptySample(t *testing.T) {
	// Zero rows still resolves to a valid single-column schema (arrow.json).
	p := inferPlan(nil)
	require.True(t, p.singleValue)
	f := field(t, p.schema, "value")
	require.True(t, isJSONField(f), "empty sample should yield an arrow.json value column")
}

// kkey builds a per-row-unique object key so the map heuristic sees id-keyed
// data rather than a fixed record.
func kkey(i int, suffix string) string {
	return string(rune('A'+i)) + suffix
}

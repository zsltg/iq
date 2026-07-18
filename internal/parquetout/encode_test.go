package parquetout

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/stretchr/testify/require"
)

// builderFor returns a fresh builder for a one-column schema of type dt, so a
// single appendVal call can be exercised in isolation.
func builderFor(t *testing.T, dt arrow.DataType) array.Builder {
	t.Helper()
	s := arrow.NewSchema([]arrow.Field{{Name: "c", Type: dt, Nullable: true}}, nil)
	rb := array.NewRecordBuilder(memory.NewGoAllocator(), s)
	t.Cleanup(rb.Release)
	return rb.Field(0)
}

func TestAppendValErrors(t *testing.T) {
	tests := []struct {
		name string
		dt   arrow.DataType
		enc  *enc
		v    any
		want string
	}{
		{
			name: "timestamp parse failure",
			dt:   &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"},
			enc:  &enc{kind: encTimestamp},
			v:    "not-a-timestamp",
			want: "not an RFC3339Nano timestamp",
		},
		{
			name: "timestamp wrong go type",
			dt:   &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"},
			enc:  &enc{kind: encTimestamp},
			v:    42,
			want: "does not fit inferred type",
		},
		{
			name: "date parse failure",
			dt:   arrow.FixedWidthTypes.Date32,
			enc:  &enc{kind: encDate},
			v:    "2021-13-99",
			want: "not a YYYY-MM-DD date",
		},
		{
			name: "int column string value",
			dt:   arrow.PrimitiveTypes.Int64,
			enc:  &enc{kind: encInt},
			v:    "thirty",
			want: `does not fit inferred type int64`,
		},
		{
			name: "int column fractional value",
			dt:   arrow.PrimitiveTypes.Int64,
			enc:  &enc{kind: encInt},
			v:    3.7,
			want: "int64",
		},
		{
			name: "float column bool value",
			dt:   arrow.PrimitiveTypes.Float64,
			enc:  &enc{kind: encFloat},
			v:    true,
			want: "double",
		},
		{
			name: "bool column int value",
			dt:   arrow.FixedWidthTypes.Boolean,
			enc:  &enc{kind: encBool},
			v:    1,
			want: "bool",
		},
		{
			name: "string column int value",
			dt:   arrow.BinaryTypes.String,
			enc:  &enc{kind: encString},
			v:    1,
			want: "utf8",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := builderFor(t, tc.dt)
			err := appendVal(b, tc.enc, tc.v, "c")
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
			require.Contains(t, err.Error(), `"c"`, "error should name the column")
			require.Contains(t, err.Error(), "jsonl", "error should point at the lossless fallback")
		})
	}
}

func TestAppendValNullAndSuccess(t *testing.T) {
	t.Run("nil is a null in any column", func(t *testing.T) {
		b := builderFor(t, arrow.PrimitiveTypes.Int64)
		require.NoError(t, appendVal(b, &enc{kind: encInt}, nil, "c"))
		arr := b.NewArray()
		defer arr.Release()
		require.True(t, arr.IsNull(0))
	})

	t.Run("integral float fits an int column", func(t *testing.T) {
		b := builderFor(t, arrow.PrimitiveTypes.Int64)
		require.NoError(t, appendVal(b, &enc{kind: encInt}, 5.0, "c"))
		arr := b.(*array.Int64Builder).NewInt64Array()
		defer arr.Release()
		require.Equal(t, int64(5), arr.Value(0))
	})

	t.Run("heterogeneous value serializes to canonical json", func(t *testing.T) {
		b := builderFor(t, arrow.BinaryTypes.String)
		require.NoError(t, appendVal(b, &enc{kind: encJSON}, map[string]any{"b": 2, "a": 1}, "c"))
		arr := b.(*array.StringBuilder).NewStringArray()
		defer arr.Release()
		require.Equal(t, `{"a":1,"b":2}`, arr.Value(0))
	})
}

func TestToInt64(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want int64
		ok   bool
	}{
		{"int", 7, 7, true},
		{"int8", int8(1), 1, true},
		{"int16", int16(2), 2, true},
		{"int32", int32(3), 3, true},
		{"int64", int64(9), 9, true},
		{"uint", uint(4), 4, true},
		{"uint8", uint8(5), 5, true},
		{"uint16", uint16(6), 6, true},
		{"uint32", uint32(7), 7, true},
		{"uint64 in range", uint64(10), 10, true},
		{"uint64 exactly max int64", uint64(math.MaxInt64), math.MaxInt64, true},
		{"uint64 one past max int64", uint64(math.MaxInt64) + 1, 0, false},
		{"uint64 overflow", uint64(math.MaxUint64), 0, false},
		{"integral float32", float32(8), 8, true},
		{"integral float", 5.0, 5, true},
		{"fractional float", 5.5, 0, false},
		{"min int64 as float", float64(math.MinInt64), math.MinInt64, true},
		{"large positive float out of range", 1e30, 0, false},
		{"large negative float out of range", -1e30, 0, false},
		{"positive infinity", math.Inf(1), 0, false},
		{"negative infinity", math.Inf(-1), 0, false},
		{"big.Int in range", big.NewInt(123), 123, true},
		{"big.Int overflow", new(big.Int).Lsh(big.NewInt(1), 100), 0, false},
		{"json.Number integer", json.Number("15"), 15, true},
		// Int64() keeps full precision here; the float fallback would round 2^63-1
		// up to 2^63 and reject it, so this pins the integer-first branch.
		{"json.Number max int64", json.Number("9223372036854775807"), math.MaxInt64, true},
		{"json.Number integral float", json.Number("16.0"), 16, true},
		{"json.Number fractional", json.Number("1.5"), 0, false},
		{"json.Number junk", json.Number("nope"), 0, false},
		{"NaN", math.NaN(), 0, false},
		{"string", "x", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := toInt64(tc.v)
			require.Equal(t, tc.ok, ok)
			if tc.ok {
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func TestCanonicalJSONNoHTMLEscape(t *testing.T) {
	got, err := canonicalJSON("a<b>&c")
	require.NoError(t, err)
	require.Equal(t, `"a<b>&c"`, got)
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want float64
		ok   bool
	}{
		{"int", 7, 7, true},
		{"int8", int8(1), 1, true},
		{"int16", int16(2), 2, true},
		{"int32", int32(3), 3, true},
		{"int64", int64(4), 4, true},
		{"uint", uint(5), 5, true},
		{"uint8", uint8(6), 6, true},
		{"uint16", uint16(7), 7, true},
		{"uint32", uint32(8), 8, true},
		{"uint64", uint64(9), 9, true},
		{"float32", float32(1.5), 1.5, true},
		{"float64", 2.5, 2.5, true},
		{"big.Int", big.NewInt(42), 42, true},
		{"json.Number", json.Number("3.5"), 3.5, true},
		{"json.Number not numeric", json.Number("x"), 0, false},
		{"string", "nope", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := toFloat64(tc.v)
			require.Equal(t, tc.ok, ok)
			if tc.ok {
				require.InDelta(t, tc.want, got, 1e-9)
			}
		})
	}
}

func TestJSONTypeName(t *testing.T) {
	tests := []struct {
		v    any
		want string
	}{
		{nil, "null"},
		{true, "bool"},
		{"s", "string"},
		{[]any{1}, "array"},
		{map[string]any{}, "object"},
		{1.5, "number"},
		{json.Number("1"), "number"},
		{7, "integer"},
		{big.NewInt(1), "integer"},
		{struct{}{}, "value"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			require.Equal(t, tc.want, jsonTypeName(tc.v))
		})
	}
}

func TestAppendCompositeMismatch(t *testing.T) {
	tests := []struct {
		name string
		dt   arrow.DataType
		e    *enc
		want string
	}{
		{
			name: "struct wants an object",
			dt:   arrow.StructOf(arrow.Field{Name: "a", Type: arrow.PrimitiveTypes.Int64, Nullable: true}),
			e:    &enc{kind: encStruct, fields: []encField{{name: "a", enc: &enc{kind: encInt}}}},
			want: "struct",
		},
		{
			name: "list wants an array",
			dt:   arrow.ListOf(arrow.PrimitiveTypes.Int64),
			e:    &enc{kind: encList, elem: &enc{kind: encInt}},
			want: "list",
		},
		{
			name: "map wants an object",
			dt:   arrow.MapOf(arrow.BinaryTypes.String, arrow.PrimitiveTypes.Int64),
			e:    &enc{kind: encMap, elem: &enc{kind: encInt}},
			want: "map",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := builderFor(t, tc.dt)
			err := appendVal(b, tc.e, "not-a-composite", "c")
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
			require.Contains(t, err.Error(), `"c"`)
		})
	}
}

func TestAppendCompositeChildError(t *testing.T) {
	// A composite whose CHILD value does not fit its type must surface the child's
	// error, so the per-element/field append loop propagates rather than swallows.
	tests := []struct {
		name string
		dt   arrow.DataType
		e    *enc
		v    any
	}{
		{
			name: "struct field mismatch",
			dt:   arrow.StructOf(arrow.Field{Name: "a", Type: arrow.PrimitiveTypes.Int64, Nullable: true}),
			e:    &enc{kind: encStruct, fields: []encField{{name: "a", enc: &enc{kind: encInt}}}},
			v:    map[string]any{"a": "not-an-int"},
		},
		{
			name: "list element mismatch",
			dt:   arrow.ListOf(arrow.PrimitiveTypes.Int64),
			e:    &enc{kind: encList, elem: &enc{kind: encInt}},
			v:    []any{1, "not-an-int"},
		},
		{
			name: "map value mismatch",
			dt:   arrow.MapOf(arrow.BinaryTypes.String, arrow.PrimitiveTypes.Int64),
			e:    &enc{kind: encMap, elem: &enc{kind: encInt}},
			v:    map[string]any{"k": "not-an-int"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := builderFor(t, tc.dt)
			err := appendVal(b, tc.e, tc.v, "c")
			require.Error(t, err)
			require.Contains(t, err.Error(), "int64")
		})
	}
}

func TestAppendJSONMarshalError(t *testing.T) {
	// A value that cannot be JSON-encoded (NaN) fails the arrow.json column,
	// naming the column and wrapping the marshal error (errors.Unwrap reachable).
	b := builderFor(t, arrow.BinaryTypes.String)
	err := appendVal(b, &enc{kind: encJSON}, math.NaN(), "c")
	require.Error(t, err)
	require.Contains(t, err.Error(), `encode "c" as json`)
	require.Error(t, errors.Unwrap(err), "the marshal error must be wrapped, not flattened")
}

func TestCanonicalJSONError(t *testing.T) {
	// NaN has no JSON representation; canonicalJSON must surface the marshal error
	// with context, wrapping the cause.
	_, err := canonicalJSON(math.NaN())
	require.Error(t, err)
	require.Contains(t, err.Error(), "marshal json")
	require.Error(t, errors.Unwrap(err), "the marshal error must be wrapped")
}

func TestAppendStructMissingFieldIsNull(t *testing.T) {
	dt := arrow.StructOf(
		arrow.Field{Name: "a", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		arrow.Field{Name: "b", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
	)
	e := &enc{kind: encStruct, fields: []encField{
		{name: "a", enc: &enc{kind: encInt}},
		{name: "b", enc: &enc{kind: encInt}},
	}}
	b := builderFor(t, dt)
	// "b" is absent from the object → a null in its child column.
	require.NoError(t, appendVal(b, e, map[string]any{"a": 5}, "s"))
	arr := b.(*array.StructBuilder).NewStructArray()
	defer arr.Release()
	require.False(t, arr.IsNull(0))
	require.Equal(t, int64(5), arr.Field(0).(*array.Int64).Value(0))
	require.True(t, arr.Field(1).IsNull(0), "missing struct field should be null")
}

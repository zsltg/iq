package file

import (
	"bytes"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// roundTrip encodes a record and decodes it back through the pinned CBOR modes,
// the exact path the cache uses.
func roundTrip(t *testing.T, rec query.Record) query.Record {
	t.Helper()
	b, err := encodeRecord(rec)
	require.NoError(t, err)
	dec := cborDec.NewDecoder(bytes.NewReader(b))
	got, err := decodeRecord(dec)
	require.NoError(t, err)
	return got
}

// TestCBORRoundTripClosedSet proves the codec reproduces every member of the
// value model Normalize emits — nil, bool, int, float64, string, map[string]any,
// []any — with identical Go types, so a warm-cache query sees the same values a
// cold one does. require.Equal uses reflect.DeepEqual, which distinguishes int
// from the int64/uint64 a naive CBOR decode would yield, so a type regression
// fails the assertion.
func TestCBORRoundTripClosedSet(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{"nil", nil},
		{"bool-true", true},
		{"bool-false", false},
		{"int-zero", int(0)},
		{"int-small", int(5)},
		{"int-negative", int(-42)},
		{"int-large", int(1 << 40)},
		{"int-max", int(math.MaxInt)},
		{"int-min", int(math.MinInt)},
		{"float", float64(1.5)},
		{"float-whole", float64(3)},
		{"float-negative", float64(-2.25)},
		{"string", "hello"},
		{"string-empty", ""},
		{"string-invalid-utf8", string([]byte{0xff, 0xfe, 0x41, 0x00, 0x80})},
		{"empty-map", map[string]any{}},
		{"empty-slice", []any{}},
		{"nested", map[string]any{
			"n":   int(7),
			"f":   float64(2.5),
			"s":   "x",
			"b":   true,
			"nil": nil,
			"arr": []any{int(1), "two", float64(3.5), nil},
			"sub": map[string]any{"k": int(-1)},
		}},
		{"invalid-utf8-map-key", map[string]any{string([]byte{0xff, 0x00}): int(9)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := query.Record{Key: "k", Type: "string", Value: tt.value}
			got := roundTrip(t, rec)
			require.Equal(t, rec, got)
		})
	}
}

// TestCBORRoundTripKeyAndType covers a record whose Key is itself binary (a Redis
// key can be arbitrary bytes), which must survive as a string.
func TestCBORRoundTripKeyAndType(t *testing.T) {
	rec := query.Record{
		Key:   string([]byte{0xde, 0xad, 0xbe, 0xef}),
		Type:  "hash",
		Value: map[string]any{"f": "v"},
	}
	require.Equal(t, rec, roundTrip(t, rec))
}

// TestCBORNaNInfPreserved special-cases the non-finite floats. A NaN round-trips
// as a float64 NaN (CBOR canonicalizes the payload bits, so it is checked with
// math.IsNaN, not equality — reflect.DeepEqual(NaN, NaN) is false anyway); ±Inf
// round-trips bit-exact. A Mongo double NaN/Inf reaches Normalize unchanged, so
// the cache must carry its non-finiteness faithfully.
func TestCBORNaNInfPreserved(t *testing.T) {
	nan := roundTrip(t, query.Record{Key: "k", Value: math.NaN()})
	f, ok := nan.Value.(float64)
	require.True(t, ok)
	require.True(t, math.IsNaN(f))

	for _, v := range []float64{math.Inf(1), math.Inf(-1)} {
		got := roundTrip(t, query.Record{Key: "k", Value: v})
		gf, ok := got.Value.(float64)
		require.True(t, ok)
		require.Equal(t, math.Float64bits(v), math.Float64bits(gf))
	}
}

// TestNarrowInts checks the integer-narrowing walk directly, including the
// out-of-int-range uint64 branch that normalized data never reaches but the
// codec must still handle without overflowing.
func TestNarrowInts(t *testing.T) {
	require.Equal(t, int(5), narrowInts(uint64(5)))
	require.Equal(t, int(-5), narrowInts(int64(-5)))
	require.Equal(t, int(0), narrowInts(uint64(0)))
	require.Equal(t, map[string]any{"a": int(1), "b": []any{int(2)}},
		narrowInts(map[string]any{"a": uint64(1), "b": []any{int64(2)}}))
	// A uint64 beyond int degrades to float64 rather than wrapping negative.
	require.Equal(t, float64(math.MaxUint64), narrowInts(uint64(math.MaxUint64)))
	// Non-integers pass through untouched.
	require.Equal(t, "s", narrowInts("s"))
	require.Equal(t, float64(1.5), narrowInts(float64(1.5)))
	require.Nil(t, narrowInts(nil))
}

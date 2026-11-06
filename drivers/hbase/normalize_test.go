package hbase

import (
	"context"
	"encoding/base64"
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tsuna/gohbase/hrpc"

	"github.com/zsltg/iq/internal/query"
)

func TestParseColType(t *testing.T) {
	tests := []struct {
		in   string
		want colType
		ok   bool
	}{
		{"text", ctText, true},
		{"string", ctText, true},
		{"bytes", ctBytes, true},
		{"binary", ctBytes, true},
		{"int", ctInt, true},
		{"long", ctLong, true},
		{"double", ctDouble, true},
		{"bool", ctBool, true},
		{"nonsense", ctAuto, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseColType(tt.in)
			if !tt.ok {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeCellAuto(t *testing.T) {
	require.Equal(t, "Dune", decodeCell(ctAuto, []byte("Dune"), nil))
	// Non-UTF-8 bytes fall back to base64.
	raw := []byte{0xff, 0xfe, 0x00}
	require.Equal(t, base64.StdEncoding.EncodeToString(raw), decodeCell(ctAuto, raw, nil))
}

func TestDecodeCellTypedRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		ct   colType
		val  any
	}{
		{"text", ctText, "hello"},
		{"bytes", ctBytes, base64.StdEncoding.EncodeToString([]byte{0x01, 0x02, 0x03})},
		{"int", ctInt, 42},
		{"int negative", ctInt, -7},
		{"int max", ctInt, math.MaxInt32},
		{"int min", ctInt, math.MinInt32},
		{"long", ctLong, 9000000000},
		{"double", ctDouble, 3.5},
		{"bool true", ctBool, true},
		{"bool false", ctBool, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := encodeCell(tt.ct, tt.val)
			require.NoError(t, err)
			require.Equal(t, tt.val, decodeCell(tt.ct, b, nil))
		})
	}
}

func TestDecodeCellWrongWidthFallsBack(t *testing.T) {
	// A declared long whose bytes are not 8 wide falls back to the honest rendering
	// rather than misreading three bytes as a number.
	require.Equal(t, "abc", decodeCell(ctLong, []byte("abc"), nil))
	require.Equal(t, "xy", decodeCell(ctInt, []byte("xy"), nil))
	require.Equal(t, "zz", decodeCell(ctBool, []byte("zz"), nil)) // 2 bytes: not a bool width
}

func TestEncodeCellRejectsBadShapes(t *testing.T) {
	tests := []struct {
		name string
		ct   colType
		val  any
	}{
		{"auto wants string", ctAuto, 42},
		{"text wants string", ctText, true},
		{"bytes wants base64", ctBytes, "not base64!!"},
		{"int wants number", ctInt, "abc"},
		{"int just over int32", ctInt, int64(math.MaxInt32) + 1},
		{"int just under int32", ctInt, int64(math.MinInt32) - 1},
		{"long wants whole", ctLong, 1.5},
		{"double wants number", ctDouble, "xyz"},
		{"bool wants bool", ctBool, "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := encodeCell(tt.ct, tt.val)
			require.Error(t, err)
		})
	}
}

func TestEncodeCellAcceptsFloatIntegers(t *testing.T) {
	// A JSON number decodes to float64, and a whole float is a valid int/long
	// literal.
	b, err := encodeCell(ctLong, float64(1234))
	require.NoError(t, err)
	require.Equal(t, 1234, decodeCell(ctLong, b, nil))
}

func TestRowFromCells(t *testing.T) {
	types := typeMap{cellKey("cf", "age"): ctLong}
	ageBytes, err := encodeCell(ctLong, 30)
	require.NoError(t, err)
	cells := []*hrpc.Cell{
		{Row: []byte("1"), Family: []byte("cf"), Qualifier: []byte("name"), Value: []byte("Ann")},
		{Row: []byte("1"), Family: []byte("cf"), Qualifier: []byte("age"), Value: ageBytes},
		{Row: []byte("1"), Family: []byte("meta"), Qualifier: []byte("v"), Value: []byte("1")},
	}
	got := rowFromCells(cells, types, nil)
	require.Equal(t, map[string]any{
		"cf":   map[string]any{"name": "Ann", "age": 30},
		"meta": map[string]any{"v": "1"},
	}, got)
}

func TestRowKeyRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		ct   colType
		key  string
	}{
		{"auto text", ctAuto, "book-1"},
		{"text", ctText, "book-2"},
		{"long", ctLong, "100"},
		{"bytes", ctBytes, base64.StdEncoding.EncodeToString([]byte{0x00, 0x01, 0xff})},
		{"bool", ctBool, "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := encodeRowKey(tt.ct, tt.key)
			require.NoError(t, err)
			require.Equal(t, tt.key, rowKeyString(context.Background(), tt.ct, b))
		})
	}
}

func TestEncodeCellBoolIsASingleCanonicalByte(t *testing.T) {
	// Bytes.toBytes(boolean) is exactly one byte, 1 or 0. Any non-zero byte decodes
	// back as true, so the round trip cannot pin this: the bytes themselves must be.
	on, err := encodeCell(ctBool, true)
	require.NoError(t, err)
	require.Equal(t, []byte{1}, on)

	off, err := encodeCell(ctBool, false)
	require.NoError(t, err)
	require.Equal(t, []byte{0}, off)
}

func TestEncodeCellKeepsTheParseCause(t *testing.T) {
	t.Run("a bytes column keeps the base64 cause", func(t *testing.T) {
		_, err := encodeCell(ctBytes, "not base64!!")
		var corrupt base64.CorruptInputError
		require.ErrorAs(t, err, &corrupt)
	})

	t.Run("a long column keeps the strconv cause", func(t *testing.T) {
		_, err := encodeCell(ctLong, "abc")
		var num *strconv.NumError
		require.ErrorAs(t, err, &num)
		require.Equal(t, "ParseInt", num.Func)
	})

	t.Run("a double column keeps the strconv cause", func(t *testing.T) {
		_, err := encodeCell(ctDouble, "xyz")
		var num *strconv.NumError
		require.ErrorAs(t, err, &num)
		require.Equal(t, "ParseFloat", num.Func)
	})
}

func TestToInt64(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		want    int64
		wantErr bool
	}{
		{name: "int", in: 7, want: 7},
		{name: "int64", in: int64(-9), want: -9},
		{name: "whole float", in: float64(1234), want: 1234},
		{name: "a string is read in base ten", in: "100", want: 100},
		{name: "the largest int64", in: strconv.FormatInt(math.MaxInt64, 10), want: math.MaxInt64},
		{name: "the smallest int64", in: strconv.FormatInt(math.MinInt64, 10), want: math.MinInt64},
		{name: "a fractional float is refused", in: 1.5, wantErr: true},
		{name: "the smallest int64 as a float", in: -9223372036854775808.0, want: math.MinInt64},
		{name: "the largest float below 2^63", in: math.Nextafter(9223372036854775808.0, 0), want: 9223372036854774784},
		{name: "an unparsable string is refused", in: "abc", wantErr: true},
		{name: "a non-number is refused", in: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toInt64(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				// A refused coercion carries no number beside its error.
				require.Zero(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		want    float64
		wantErr bool
	}{
		{name: "float64", in: 3.5, want: 3.5},
		{name: "int", in: 7, want: 7},
		{name: "int64", in: int64(-2), want: -2},
		{name: "a decimal string", in: "3.5", want: 3.5},
		{name: "an exponent string", in: "1e300", want: 1e300},
		{name: "an unparsable string is refused", in: "xyz", wantErr: true},
		{name: "a non-number is refused", in: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toFloat64(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				// A refused coercion carries no number beside its error.
				require.Zero(t, got)
				return
			}
			require.NoError(t, err)
			// A zero delta is exact equality. A double column must not round.
			require.InDelta(t, tt.want, got, 0)
		})
	}
}

func TestToInt64RefusesAnInexactFloat(t *testing.T) {
	tests := []struct {
		name    string
		in      float64
		wantErr string
	}{
		{"positive infinity", math.Inf(1), "hbase: +Inf is not a finite whole number for an int/long column"},
		{"negative infinity", math.Inf(-1), "hbase: -Inf is not a finite whole number for an int/long column"},
		{"NaN", math.NaN(), "hbase: NaN is not a finite whole number for an int/long column"},
		{"2^63", 9223372036854775808.0, "hbase: 9.223372036854776e+18 is out of range for an int/long column"},
		{"below -2^63", -9223372036854777856.0, "hbase: -9.223372036854778e+18 is out of range for an int/long column"},
		{"1e30", 1e30, "hbase: 1e+30 is out of range for an int/long column"},
		{"fraction", 1.5, "hbase: 1.5 is not a whole number for an int/long column"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toInt64(tt.in)
			require.EqualError(t, err, tt.wantErr)
			require.Zero(t, got)
			_, err = encodeInt32(tt.in)
			require.EqualError(t, err, tt.wantErr)
			_, err = encodeInt64(tt.in)
			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestEncodeInt32FloatBounds(t *testing.T) {
	b, err := encodeInt32(float64(math.MaxInt32))
	require.NoError(t, err)
	require.Equal(t, []byte{0x7f, 0xff, 0xff, 0xff}, b)
	_, err = encodeInt32(float64(math.MaxInt32) + 1)
	require.EqualError(t, err, "hbase: value 2147483648 out of range for a 4-byte int column")
}

func TestRowKeyStringReportsKeysThatCannotBeReversed(t *testing.T) {
	tests := []struct {
		name string
		ct   colType
		key  []byte
		show string
		note bool
	}{
		{"auto, valid UTF-8", ctAuto, []byte("r1"), "r1", false},
		{"auto, invalid UTF-8", ctAuto, []byte{0xff}, "/w==", true},
		{"text, valid UTF-8", ctText, []byte("r1"), "r1", false},
		{"text, invalid UTF-8", ctText, []byte{0xff}, "\xff", true},
		{"bytes, invalid UTF-8", ctBytes, []byte{0xff}, "/w==", false},
		{"long, 8 bytes", ctLong, []byte{0, 0, 0, 0, 0, 0, 0, 7}, "7", false},
		{"long, 3 bytes", ctLong, []byte("abc"), "abc", true},
		{"long, 9 bytes", ctLong, []byte("abcdefghi"), "abcdefghi", true},
		{"int, 4 bytes", ctInt, []byte{0, 0, 0, 7}, "7", false},
		{"int, 3 bytes", ctInt, []byte("abc"), "abc", true},
		{"int, 5 bytes", ctInt, []byte("abcde"), "abcde", true},
		{"double, 8 bytes", ctDouble, []byte{0x3f, 0xf0, 0, 0, 0, 0, 0, 0}, "1", false},
		{"double, 7 bytes", ctDouble, []byte("abcdefg"), "abcdefg", true},
		{"double, 9 bytes", ctDouble, []byte("abcdefghi"), "abcdefghi", true},
		{"bool, 1 byte", ctBool, []byte{1}, "true", false},
		{"bool, 0 bytes", ctBool, []byte{}, "", true},
		{"bool, 2 bytes", ctBool, []byte("ab"), "ab", true},
		{"long, invalid UTF-8", ctLong, []byte{0xff, 0xfe}, "//4=", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var shown []string
			ctx := query.WithRowKeyNotice(context.Background(), func(s string) { shown = append(shown, s) })

			got := rowKeyString(ctx, tt.ct, tt.key)

			require.Equal(t, tt.show, got)
			if tt.note {
				require.Equal(t, []string{tt.show}, shown)
			} else {
				require.Empty(t, shown)
			}
		})
	}
}

package hbase

import (
	"encoding/base64"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tsuna/gohbase/hrpc"
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
	require.Equal(t, "Dune", decodeCell(ctAuto, []byte("Dune")))
	// Non-UTF-8 bytes fall back to base64.
	raw := []byte{0xff, 0xfe, 0x00}
	require.Equal(t, base64.StdEncoding.EncodeToString(raw), decodeCell(ctAuto, raw))
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
			require.Equal(t, tt.val, decodeCell(tt.ct, b))
		})
	}
}

func TestDecodeCellWrongWidthFallsBack(t *testing.T) {
	// A declared long whose bytes are not 8 wide falls back to the honest rendering
	// rather than misreading three bytes as a number.
	require.Equal(t, "abc", decodeCell(ctLong, []byte("abc")))
	require.Equal(t, "xy", decodeCell(ctInt, []byte("xy")))
	require.Equal(t, "zz", decodeCell(ctBool, []byte("zz"))) // 2 bytes: not a bool width
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
	// A JSON number decodes to float64; a whole float is a valid int/long literal.
	b, err := encodeCell(ctLong, float64(1234))
	require.NoError(t, err)
	require.Equal(t, 1234, decodeCell(ctLong, b))
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
	got := rowFromCells(cells, types)
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
			require.Equal(t, tt.key, rowKeyString(tt.ct, b))
		})
	}
}

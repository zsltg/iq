package mongo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestNormalize(t *testing.T) {
	oid, err := bson.ObjectIDFromHex("507f1f77bcf86cd799439011")
	require.NoError(t, err)
	when := time.Date(2017, 3, 14, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		mode numfmt.DecimalMode
		in   any
		want any
	}{
		{name: "nil", in: nil, want: nil},
		{name: "bool", in: true, want: true},
		{name: "string", in: "hi", want: "hi"},
		{name: "float64", in: 1.5, want: 1.5},
		{name: "int32 to int", in: int32(42), want: 42},
		{name: "int64 to int", in: int64(2015), want: 2015},
		{name: "objectid to hex", in: oid, want: "507f1f77bcf86cd799439011"},
		{name: "datetime to rfc3339", in: bson.NewDateTimeFromTime(when), want: "2017-03-14T09:30:00Z"},
		{name: "decimal auto keeps exact string", mode: numfmt.DecimalAuto, in: mustDecimal(t, "3.14"), want: "3.14"},
		{name: "decimal string keeps exact string", mode: numfmt.DecimalString, in: mustDecimal(t, "3.14"), want: "3.14"},
		{name: "decimal number becomes a float", mode: numfmt.DecimalNumber, in: mustDecimal(t, "3.14"), want: 3.14},
		{name: "negative decimal number becomes a float", mode: numfmt.DecimalNumber, in: mustDecimal(t, "-2.5"), want: -2.5},
		{name: "high-precision decimal string is exact", mode: numfmt.DecimalString, in: mustDecimal(t, "1.0000000000000000001"), want: "1.0000000000000000001"},
		{name: "high-precision decimal number is a float", mode: numfmt.DecimalNumber, in: mustDecimal(t, "1.0000000000000000001"), want: 1.0000000000000000001},
		{name: "decimal NaN stays a string in number mode", mode: numfmt.DecimalNumber, in: mustDecimal(t, "NaN"), want: "NaN"},
		{name: "binary to base64", in: bson.Binary{Subtype: 0, Data: []byte("hi")}, want: "aGk="},
		{
			name: "nested document",
			in:   bson.M{"title": "Go", "meta": bson.M{"year": int32(2015)}},
			want: map[string]any{"title": "Go", "meta": map[string]any{"year": 2015}},
		},
		{
			name: "ordered document D",
			in:   bson.D{{Key: "a", Value: int32(1)}, {Key: "b", Value: "x"}},
			want: map[string]any{"a": 1, "b": "x"},
		},
		{
			name: "array of mixed",
			in:   bson.A{int32(1), "two", oid},
			want: []any{1, "two", "507f1f77bcf86cd799439011"},
		},
		{
			name: "decimal inside a document follows the mode",
			mode: numfmt.DecimalNumber,
			in:   bson.M{"price": mustDecimal(t, "9.99")},
			want: map[string]any{"price": 9.99},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Store{decimal: tt.mode}
			require.Equal(t, tt.want, s.normalize(tt.in))
		})
	}
}

func TestKeyOf(t *testing.T) {
	oid, err := bson.ObjectIDFromHex("507f1f77bcf86cd799439011")
	require.NoError(t, err)

	tests := []struct {
		name string
		in   any
		want string
	}{
		{"objectid to hex", oid, "507f1f77bcf86cd799439011"},
		{"string is itself", "book-1", "book-1"},
		{"int32", int32(7), "7"},
		{"int64", int64(7), "7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, KeyOf(tt.in))
		})
	}
}

func TestIDValues(t *testing.T) {
	hex := "507f1f77bcf86cd799439011"
	oid, err := bson.ObjectIDFromHex(hex)
	require.NoError(t, err)

	// A plain key matches only as a string.
	require.Equal(t, bson.A{"book-1"}, idValues([]string{"book-1"}))
	// A 24-char hex key matches as both a string and the ObjectID it encodes.
	require.Equal(t, bson.A{hex, oid}, idValues([]string{hex}))
}

func mustDecimal(t *testing.T, s string) bson.Decimal128 {
	t.Helper()
	d, err := bson.ParseDecimal128(s)
	require.NoError(t, err)
	return d
}

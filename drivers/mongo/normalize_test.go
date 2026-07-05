package mongo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestNormalize(t *testing.T) {
	oid, err := bson.ObjectIDFromHex("507f1f77bcf86cd799439011")
	require.NoError(t, err)
	when := time.Date(2017, 3, 14, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		in   any
		want any
	}{
		{"nil", nil, nil},
		{"bool", true, true},
		{"string", "hi", "hi"},
		{"float64", 1.5, 1.5},
		{"int32 to int", int32(42), 42},
		{"int64 to int", int64(2015), 2015},
		{"objectid to hex", oid, "507f1f77bcf86cd799439011"},
		{"datetime to rfc3339", bson.NewDateTimeFromTime(when), "2017-03-14T09:30:00Z"},
		{"decimal to string", mustDecimal(t, "3.14"), "3.14"},
		{"binary to base64", bson.Binary{Subtype: 0, Data: []byte("hi")}, "aGk="},
		{
			"nested document",
			bson.M{"title": "Go", "meta": bson.M{"year": int32(2015)}},
			map[string]any{"title": "Go", "meta": map[string]any{"year": 2015}},
		},
		{
			"ordered document D",
			bson.D{{Key: "a", Value: int32(1)}, {Key: "b", Value: "x"}},
			map[string]any{"a": 1, "b": "x"},
		},
		{
			"array of mixed",
			bson.A{int32(1), "two", oid},
			[]any{1, "two", "507f1f77bcf86cd799439011"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, normalize(tt.in))
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
			require.Equal(t, tt.want, keyOf(tt.in))
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

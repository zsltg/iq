package mongo

import (
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

func TestDocumentFor(t *testing.T) {
	tests := []struct {
		name    string
		rec     query.Record
		want    bson.M
		wantErr bool
	}{
		{
			name: "object value keeps fields, key becomes _id",
			rec:  query.Record{Key: "k1", Value: map[string]any{"title": "Dune"}},
			want: bson.M{"_id": "k1", "title": "Dune"},
		},
		{
			name: "read _id is replaced by the record key",
			rec:  query.Record{Key: "k1", Value: map[string]any{"_id": "stale", "title": "Dune"}},
			want: bson.M{"_id": "k1", "title": "Dune"},
		},
		{
			name: "keyless object mints its own _id",
			rec:  query.Record{Value: map[string]any{"title": "Dune"}},
			want: bson.M{"title": "Dune"},
		},
		{
			// The key is the only identity, so a keyless record must drop the _id its
			// value carries and let MongoDB mint one.
			name: "keyless object drops a read _id",
			rec:  query.Record{Value: map[string]any{"_id": "stale", "title": "Dune"}},
			want: bson.M{"title": "Dune"},
		},
		{
			name:    "scalar value is rejected, not wrapped",
			rec:     query.Record{Key: "k2", Value: "hello"},
			wantErr: true,
		},
		{
			name:    "keyless scalar is rejected too",
			rec:     query.Record{Value: 42},
			wantErr: true,
		},
		{
			name: "hex key restores an ObjectID _id",
			rec:  query.Record{Key: "507f1f77bcf86cd799439011", Value: map[string]any{"n": 1}},
			want: bson.M{"_id": mustOID(t, "507f1f77bcf86cd799439011"), "n": 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := documentFor(tt.rec)
			if tt.wantErr {
				require.ErrorContains(t, err, "is not a JSON object")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDocumentForBigInt(t *testing.T) {
	d34 := "1234567890123456789012345678901234"
	tests := []struct {
		name string
		in   string
		want any    // an int64, or the Decimal128 text that a read gives back
		err  string // empty means valid
	}{
		{"small", "42", int64(42), ""},
		{"max int64", "9223372036854775807", int64(math.MaxInt64), ""},
		{"min int64", "-9223372036854775808", int64(math.MinInt64), ""},
		{"one past max int64", "9223372036854775808", "9223372036854775808", ""},
		{"one past min int64", "-9223372036854775809", "-9223372036854775809", ""},
		{"30 digits", "123456789012345678901234567890", "123456789012345678901234567890", ""},
		{"34 digits", d34, d34, ""},
		{"34 digits negative", "-" + d34, "-" + d34, ""},
		{"34 digits with a trailing zero", "1234567890123456789012345678901230", "1234567890123456789012345678901230", ""},
		{"35 digits", d34 + "5", nil, "has more digits than a Decimal128 holds (34)"},
		{"35 digits negative", "-" + d34 + "5", nil, "has more digits than a Decimal128 holds (34)"},
		{"35 digits then zeros", d34 + "5000", nil, "integer 1234567890... (38 digits) has more digits"},
		{"34 digits then zeros", d34 + "00000", "1.234567890123456789012345678901234E+38", ""},
		{"one with 40 zeros", "1" + strings.Repeat("0", 40), "1E+40", ""},
		{"negative one with 40 zeros", "-1" + strings.Repeat("0", 40), "-1E+40", ""},
		{"past the stored exponent", "1" + strings.Repeat("0", 6112), "1.0E+6112", ""},
		{"largest power of ten", "1" + strings.Repeat("0", 6144), "1.000000000000000000000000000000000E+6144", ""},
		{"34 digits at the largest exponent", d34 + strings.Repeat("0", 6111), "1.234567890123456789012345678901234E+6144", ""},
		{"past the largest power of ten", "1" + strings.Repeat("0", 6145), nil, "integer 1000000000... (6146 digits) is too large for a Decimal128"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, ok := new(big.Int).SetString(tt.in, 10)
			require.True(t, ok)
			doc, err := documentFor(query.Record{Key: "k", Value: map[string]any{"a": map[string]any{"b": []any{n}}}})
			if tt.err != "" {
				require.ErrorContains(t, err, `mongodb: key "k": field "a.b[0]": integer `)
				require.ErrorContains(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			got := doc["a"].(bson.M)["b"].(bson.A)[0]
			if want, ok := tt.want.(int64); ok {
				require.Equal(t, want, got)
				return
			}
			d, ok := got.(bson.Decimal128)
			require.True(t, ok)
			// The read path gives the exact Decimal128 text, in auto and string mode.
			require.Equal(t, tt.want, decimalValue(d, numfmt.DecimalAuto))
			// The stored value equals the input.
			back, ok := new(big.Float).SetPrec(40000).SetString(d.String())
			require.True(t, ok)
			orig, _ := new(big.Float).SetPrec(40000).SetString(tt.in)
			require.Zero(t, back.Cmp(orig))
		})
	}
}

func TestDocumentForBigIntNamesTheFirstBadFieldInSortedOrder(t *testing.T) {
	over := new(big.Int).Exp(big.NewInt(10), big.NewInt(40), nil)
	over.Add(over, big.NewInt(1))
	// Two bad fields: the error names the first by sorted field name.
	_, err := documentFor(query.Record{Key: "k", Value: map[string]any{"z": over, "b": over, "_id": over}})
	require.EqualError(t, err, `mongodb: key "k": field "b": integer 1000000000... (41 digits) has more digits than a Decimal128 holds (34)`)
}

func TestIDValue(t *testing.T) {
	require.Equal(t, "plain-key", idValue("plain-key"))
	require.Equal(t, mustOID(t, "507f1f77bcf86cd799439011"), idValue("507f1f77bcf86cd799439011"))
}

func TestExplainWrite(t *testing.T) {
	require.Contains(t, ExplainWrite(query.Upsert).Ops[0], "replaceOne upsert")
	require.Contains(t, ExplainWrite(query.InsertOnly).Ops[0], "insertMany")
}

func TestExplainClearDrop(t *testing.T) {
	require.Contains(t, ExplainClear().Ops[0], "deleteMany")
	plan, ok := ExplainDrop()
	require.True(t, ok)
	require.Contains(t, plan.Ops[0], "drop()")
}

func TestExplainDelete(t *testing.T) {
	plan, ok := ExplainDelete()
	require.True(t, ok)
	require.Contains(t, plan.Ops[0], "deleteMany")
}

func mustOID(t *testing.T, hex string) bson.ObjectID {
	t.Helper()
	oid, err := bson.ObjectIDFromHex(hex)
	require.NoError(t, err)
	return oid
}

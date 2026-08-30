package mongo

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

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

func TestBSONValueBigInt(t *testing.T) {
	small := big.NewInt(42)
	require.Equal(t, int64(42), bsonValue(small))

	huge, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	require.Equal(t, "123456789012345678901234567890", bsonValue(huge))

	// Recurses into nested structures.
	got := bsonValue(map[string]any{"n": big.NewInt(7), "a": []any{big.NewInt(8)}})
	require.Equal(t, bson.M{"n": int64(7), "a": bson.A{int64(8)}}, got)
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

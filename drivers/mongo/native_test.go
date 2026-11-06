package mongo

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/query"
)

// nativeRaw marshals a document to raw BSON.
func nativeRaw(t *testing.T, doc bson.D) bson.Raw {
	t.Helper()
	b, err := bson.Marshal(doc)
	require.NoError(t, err)
	return b
}

func TestNativeRecordRoundTripsEveryType(t *testing.T) {
	oid := bson.NewObjectID()
	dec, err := bson.ParseDecimal128("1.50")
	require.NoError(t, err)
	tests := []struct {
		name  string
		value any
		tag   any
	}{
		{"int32", int32(7), map[string]any{"$numberInt": "7"}},
		{"int64", int64(7), map[string]any{"$numberLong": "7"}},
		{"double", 1.5, map[string]any{"$numberDouble": "1.5"}},
		{"decimal128", dec, map[string]any{"$numberDecimal": "1.50"}},
		{"date", bson.DateTime(1700000000000), map[string]any{"$date": map[string]any{"$numberLong": "1700000000000"}}},
		{"object id outside _id", oid, map[string]any{"$oid": oid.Hex()}},
		{"binary", bson.Binary{Subtype: 0, Data: []byte{0, 1, 255}}, map[string]any{"$binary": map[string]any{"base64": "AAH/", "subType": "00"}}},
		{"timestamp", bson.Timestamp{T: 5, I: 2}, map[string]any{"$timestamp": map[string]any{"t": json.Number("5"), "i": json.Number("2")}}},
		{"regex", bson.Regex{Pattern: "^a", Options: "i"}, map[string]any{"$regularExpression": map[string]any{"pattern": "^a", "options": "i"}}},
		{"min key", bson.MinKey{}, map[string]any{"$minKey": json.Number("1")}},
		{"max key", bson.MaxKey{}, map[string]any{"$maxKey": json.Number("1")}},
		{"null", nil, nil},
		{"string", "x", "x"},
		{"bool", true, true},
		{"nested", bson.D{{Key: "n", Value: bson.A{int32(1), int64(2)}}}, map[string]any{"n": []any{map[string]any{"$numberInt": "1"}, map[string]any{"$numberLong": "2"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := bson.D{{Key: "_id", Value: "k"}, {Key: "v", Value: tt.value}}
			rec, err := NativeRecord(nativeRaw(t, doc))
			require.NoError(t, err)
			require.Equal(t, "k", rec.Key)
			require.Equal(t, query.FormMongoEJSON, rec.Form)
			require.Equal(t, map[string]any{"_id": "k", "v": tt.tag}, rec.Value)

			back, err := nativeDocument(rec)
			require.NoError(t, err)
			require.Equal(t, doc, back)
		})
	}
}

func TestNativeRecordKeepsAMissingKeyApartFromNull(t *testing.T) {
	withNull, err := NativeRecord(nativeRaw(t, bson.D{{Key: "_id", Value: "a"}, {Key: "x", Value: nil}}))
	require.NoError(t, err)
	require.Contains(t, withNull.Value, "x")
	missing, err := NativeRecord(nativeRaw(t, bson.D{{Key: "_id", Value: "a"}}))
	require.NoError(t, err)
	require.NotContains(t, missing.Value, "x")

	doc, err := nativeDocument(withNull)
	require.NoError(t, err)
	require.Equal(t, bson.D{{Key: "_id", Value: "a"}, {Key: "x", Value: nil}}, doc)
}

func TestNativeRecordKeys(t *testing.T) {
	oid := bson.NewObjectID()
	tests := []struct {
		name string
		id   any
		want string
	}{
		{"object id", oid, oid.Hex()},
		{"int32", int32(7), "7"},
		{"int64", int64(8), "8"},
		{"string", "s", "s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, err := NativeRecord(nativeRaw(t, bson.D{{Key: "_id", Value: tt.id}}))
			require.NoError(t, err)
			require.Equal(t, tt.want, rec.Key)
			require.Equal(t, "document", rec.Type)
		})
	}
}

func TestNativeDocumentTypedID(t *testing.T) {
	oid := bson.NewObjectID()
	tests := []struct {
		name  string
		key   string
		value map[string]any
		want  any
	}{
		{"int32 id matches the key", "7", map[string]any{"_id": map[string]any{"$numberInt": "7"}}, int32(7)},
		{"int64 id matches the key", "7", map[string]any{"_id": map[string]any{"$numberLong": "7"}}, int64(7)},
		{"object id matches the key", oid.Hex(), map[string]any{"_id": map[string]any{"$oid": oid.Hex()}}, oid},
		{"key wins when it differs", "8", map[string]any{"_id": map[string]any{"$numberInt": "7"}}, "8"},
		{"key wins as an object id", oid.Hex(), map[string]any{"_id": map[string]any{"$numberInt": "7"}}, oid},
		{"key fills a missing id", "k", map[string]any{}, "k"},
		{"keyless keeps the id", "", map[string]any{"_id": map[string]any{"$numberInt": "7"}}, int32(7)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := nativeDocument(query.Record{Key: tt.key, Form: query.FormMongoEJSON, Value: tt.value})
			require.NoError(t, err)
			require.Equal(t, bson.E{Key: "_id", Value: tt.want}, doc[0])
		})
	}
}

func TestNativeDocumentKeylessWithoutIDGetsNone(t *testing.T) {
	doc, err := nativeDocument(query.Record{Form: query.FormMongoEJSON, Value: map[string]any{"a": "b"}})
	require.NoError(t, err)
	require.Equal(t, bson.D{{Key: "a", Value: "b"}}, doc)
}

func TestNativeDocumentRefusals(t *testing.T) {
	tests := []struct {
		name    string
		rec     query.Record
		wantErr string
	}{
		{"bad int tag", query.Record{Key: "k1", Value: map[string]any{"n": map[string]any{"$numberInt": "abc"}}}, `key "k1": value is not valid canonical Extended JSON`},
		{"bad oid", query.Record{Key: "k2", Value: map[string]any{"o": map[string]any{"$oid": "zz"}}}, `key "k2": value is not valid canonical Extended JSON`},
		{"bad date", query.Record{Key: "k3", Value: map[string]any{"d": map[string]any{"$date": true}}}, `key "k3": value is not valid canonical Extended JSON`},
		{"bad decimal", query.Record{Key: "k4", Value: map[string]any{"d": map[string]any{"$numberDecimal": "x"}}}, `key "k4": value is not valid canonical Extended JSON`},
		{"not an object", query.Record{Key: "k5", Value: "text"}, `value for key "k5" is not a JSON object`},
		{"unencodable", query.Record{Key: "k6", Value: map[string]any{"c": make(chan int)}}, `key "k6": read native value`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.rec.Form = query.FormMongoEJSON
			_, err := nativeDocument(tt.rec)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestRecordDocumentPicksTheReadingByForm(t *testing.T) {
	plain, plainID, err := recordDocument(query.Record{Key: "k", Value: map[string]any{"n": 7}})
	require.NoError(t, err)
	require.Equal(t, bson.M{"_id": "k", "n": 7}, plain)
	require.Equal(t, "k", plainID)

	native, nativeID, err := recordDocument(query.Record{Key: "k", Form: query.FormMongoEJSON, Value: map[string]any{"n": map[string]any{"$numberInt": "7"}}})
	require.NoError(t, err)
	require.Equal(t, bson.D{{Key: "_id", Value: "k"}, {Key: "n", Value: int32(7)}}, native)
	require.Equal(t, "k", nativeID)

	typed, typedID, err := recordDocument(query.Record{Key: "7", Form: query.FormMongoEJSON, Value: map[string]any{"_id": map[string]any{"$numberInt": "7"}}})
	require.NoError(t, err)
	require.Equal(t, int32(7), typedID)
	require.Equal(t, bson.D{{Key: "_id", Value: int32(7)}}, typed)
}

func TestNativeFormsOfTheStore(t *testing.T) {
	s := &Store{}
	require.Equal(t, query.FormMongoEJSON, s.NativeForm())
	require.Equal(t, query.FormMongoEJSON, query.WriterForm(s))
	var _ query.NativeReader = s
}

func TestNativeScanErrors(t *testing.T) {
	tests := []struct {
		name    string
		store   func(t *testing.T) *Store
		wantErr string
	}{
		{"no collection", func(*testing.T) *Store { return &Store{} }, "no collection selected"},
		{"unreachable server", lazyStore, "mongodb find"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.store(t).NativeScan(context.Background(), func([]query.Record) error { return nil })
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

package mongo

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
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

			back, _, err := nativeDocument(rec)
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

	doc, _, err := nativeDocument(withNull)
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
		{"a key like the display of nil", "<nil>", map[string]any{"x": "y"}, "<nil>"},
		{"keyless keeps the id", "", map[string]any{"_id": map[string]any{"$numberInt": "7"}}, int32(7)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, id, err := nativeDocument(query.Record{Key: tt.key, Form: query.FormMongoEJSON, Value: tt.value})
			require.NoError(t, err)
			require.Equal(t, bson.E{Key: "_id", Value: tt.want}, doc[0])
			require.Equal(t, tt.want, id)
		})
	}
}

func TestNativeDocumentKeylessWithoutIDGetsNone(t *testing.T) {
	doc, id, err := nativeDocument(query.Record{Form: query.FormMongoEJSON, Value: map[string]any{"a": "b"}})
	require.NoError(t, err)
	require.Equal(t, bson.D{{Key: "a", Value: "b"}}, doc)
	require.Nil(t, id)
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
		{"relaxed date", query.Record{Key: "k7", Value: map[string]any{"d": map[string]any{"$date": "2023-11-14T22:13:20Z"}}}, `key "k7": value is not valid canonical Extended JSON`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.rec.Form = query.FormMongoEJSON
			_, _, err := nativeDocument(tt.rec)
			require.ErrorContains(t, err, tt.wantErr)
			if tt.wantErr != `value for key "k5" is not a JSON object` {
				require.Error(t, errors.Unwrap(err), "the error must wrap its cause")
			}
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
		wraps   bool
	}{
		{"no collection", func(*testing.T) *Store { return &Store{} }, "no collection selected", false},
		{"unreachable server", lazyStore, "mongodb find", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.store(t).NativeScan(context.Background(), func([]query.Record) error { return nil })
			require.ErrorContains(t, err, tt.wantErr)
			if tt.wraps {
				require.Error(t, errors.Unwrap(err), "the error must wrap its cause")
			}
		})
	}
}

func TestNativeRecordOfAnInvalidDocument(t *testing.T) {
	good := nativeRaw(t, bson.D{{Key: "_id", Value: "a"}, {Key: "s", Value: "x"}})
	tests := []struct {
		name string
		doc  bson.Raw
		key  string
	}{
		{"too short", bson.Raw{1, 2, 3}, "<nil>"},
		{"cut off", good[:len(good)-2], "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NativeRecord(tt.doc)
			require.ErrorContains(t, err, `mongodb: document "`+tt.key+`": encode extended json: `)
			require.Error(t, errors.Unwrap(err), "the error must wrap its cause")
		})
	}
}

func TestNativeRecordKeyOfOddIDs(t *testing.T) {
	tests := []struct {
		name string
		doc  bson.D
		want string
	}{
		{"double id", bson.D{{Key: "_id", Value: 1.5}}, "1.5"},
		{"no id", bson.D{{Key: "a", Value: "b"}}, "<nil>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, err := NativeRecord(nativeRaw(t, tt.doc))
			require.NoError(t, err)
			require.Equal(t, tt.want, rec.Key)
		})
	}
}

// fakeCursor feeds pageNative with n documents.
type fakeCursor struct {
	n, at int
	err   error
	docs  []bson.Raw
	// ctxs holds the context of each next call.
	ctxs []context.Context
}

func newFakeCursor(t *testing.T, n int, err error) *fakeCursor {
	t.Helper()
	c := &fakeCursor{n: n, err: err}
	for i := range n {
		c.docs = append(c.docs, nativeRaw(t, bson.D{{Key: "_id", Value: int32(i)}}))
	}
	return c
}

func (c *fakeCursor) next(ctx context.Context) bool {
	c.ctxs = append(c.ctxs, ctx)
	if c.at >= c.n {
		return false
	}
	c.at++
	return true
}
func (c *fakeCursor) current() bson.Raw { return c.docs[c.at-1] }
func (c *fakeCursor) fail() error       { return c.err }

func TestPageNativePages(t *testing.T) {
	tests := []struct {
		name string
		docs int
		size int
		want []int
	}{
		{"no documents", 0, 3, nil},
		{"fewer than a page", 2, 3, []int{2}},
		{"exactly a page", 3, 3, []int{3}},
		{"a page and one", 4, 3, []int{3, 1}},
		{"two pages", 6, 3, []int{3, 3}},
		{"a page of one", 2, 1, []int{1, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newFakeCursor(t, tt.docs, nil)
			var got []int
			var keys []string
			err := pageNative(context.Background(), tt.size, c.next, c.current, c.fail, func(b []query.Record) error {
				got = append(got, len(b))
				for _, r := range b {
					keys = append(keys, r.Key)
				}
				return nil
			})
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Len(t, keys, tt.docs)
			for i, k := range keys {
				require.Equal(t, strconv.Itoa(i), k)
			}
		})
	}
}

func TestPageNativeStopsOnErrors(t *testing.T) {
	errFn := errors.New("fn failed")
	errCursor := errors.New("cursor failed")
	t.Run("fn error on a full page", func(t *testing.T) {
		c := newFakeCursor(t, 5, nil)
		calls := 0
		err := pageNative(context.Background(), 2, c.next, c.current, c.fail, func([]query.Record) error {
			calls++
			return errFn
		})
		require.ErrorIs(t, err, errFn)
		require.Equal(t, 1, calls)
	})
	t.Run("fn error on the last page", func(t *testing.T) {
		c := newFakeCursor(t, 1, nil)
		err := pageNative(context.Background(), 2, c.next, c.current, c.fail, func([]query.Record) error { return errFn })
		require.ErrorIs(t, err, errFn)
	})
	t.Run("cursor error", func(t *testing.T) {
		c := newFakeCursor(t, 1, errCursor)
		calls := 0
		err := pageNative(context.Background(), 5, c.next, c.current, c.fail, func([]query.Record) error {
			calls++
			return nil
		})
		require.ErrorIs(t, err, errCursor)
		require.ErrorContains(t, err, "mongodb cursor: ")
		require.Zero(t, calls)
	})
	t.Run("bad document", func(t *testing.T) {
		c := newFakeCursor(t, 2, nil)
		c.docs[1] = bson.Raw{1, 2, 3}
		calls := 0
		err := pageNative(context.Background(), 5, c.next, c.current, c.fail, func([]query.Record) error {
			calls++
			return nil
		})
		require.ErrorContains(t, err, "encode extended json")
		require.Zero(t, calls)
	})
}

// TestPageNativePassesItsContext proves that every cursor step gets the context
// of the caller.
func TestPageNativePassesItsContext(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "mine")
	c := newFakeCursor(t, 3, nil)
	err := pageNative(ctx, 2, c.next, c.current, c.fail, func([]query.Record) error { return nil })
	require.NoError(t, err)
	require.Len(t, c.ctxs, 4)
	for _, got := range c.ctxs {
		require.Equal(t, ctx, got)
	}
}

package mongo

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/query"
)

// TestNativeCopyKeepsEveryTypeIntegration copies a collection with NativeScan and
// Put, then compares the stored BSON of both collections.
func TestNativeCopyKeepsEveryTypeIntegration(t *testing.T) {
	src := openIntegration(t, "native_src")
	dst := openIntegration(t, "native_dst")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, src.Clear(ctx))
	require.NoError(t, dst.Clear(ctx))

	dec, err := bson.ParseDecimal128("1.50")
	require.NoError(t, err)
	oid := bson.NewObjectID()
	docs := []any{
		bson.D{{Key: "_id", Value: int32(7)}, {Key: "small", Value: int32(1)}, {Key: "big", Value: int64(2)}, {Key: "dec", Value: dec}},
		bson.D{{Key: "_id", Value: oid}, {Key: "when", Value: bson.DateTime(1700000000000)}, {Key: "ref", Value: oid}, {Key: "none", Value: nil}},
		bson.D{
			{Key: "_id", Value: "s"},
			{Key: "bin", Value: bson.Binary{Data: []byte{0, 1, 255}}},
			{Key: "ts", Value: bson.Timestamp{T: 5, I: 2}},
			{Key: "re", Value: bson.Regex{Pattern: "^a", Options: "i"}},
			{Key: "lo", Value: bson.MinKey{}},
			{Key: "hi", Value: bson.MaxKey{}},
		},
	}
	_, err = src.db.Collection(src.collection).InsertMany(ctx, docs)
	require.NoError(t, err)

	var recs []query.Record
	require.NoError(t, src.NativeScan(ctx, func(batch []query.Record) error {
		recs = append(recs, batch...)
		return nil
	}))
	require.Len(t, recs, 3)
	_, err = dst.Put(ctx, recs, query.Upsert)
	require.NoError(t, err)

	for _, d := range docs {
		id := d.(bson.D)[0].Value
		var want, got bson.M
		require.NoError(t, src.db.Collection(src.collection).FindOne(ctx, bson.M{"_id": id}).Decode(&want))
		require.NoError(t, dst.db.Collection(dst.collection).FindOne(ctx, bson.M{"_id": id}).Decode(&got), "id %v", id)
		require.Equal(t, want, got, "id %v", id)
	}
}

// TestNativeScanPagesIntegration proves that the scan hands out pages no larger
// than the page size.
func TestNativeScanPagesIntegration(t *testing.T) {
	store := openIntegration(t, "native_pages")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, store.Clear(ctx))
	docs := make([]any, 25)
	for i := range docs {
		docs[i] = bson.D{{Key: "_id", Value: fmt.Sprintf("k%02d", i)}}
	}
	_, err := store.db.Collection(store.collection).InsertMany(ctx, docs)
	require.NoError(t, err)
	store.pageSize = 10

	var sizes []int
	require.NoError(t, store.NativeScan(ctx, func(batch []query.Record) error {
		sizes = append(sizes, len(batch))
		return nil
	}))
	require.Equal(t, []int{10, 10, 5}, sizes)
}

// TestNativePutKeepsTheTypedIDIntegration writes an int32 _id and a mismatched
// key, and reads the stored ids back.
func TestNativePutKeepsTheTypedIDIntegration(t *testing.T) {
	store := openIntegration(t, "native_id")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, store.Clear(ctx))
	_, err := store.Put(ctx, []query.Record{
		{Key: "7", Form: query.FormMongoEJSON, Value: map[string]any{"_id": map[string]any{"$numberInt": "7"}}},
		{Key: "8", Form: query.FormMongoEJSON, Value: map[string]any{"_id": map[string]any{"$numberInt": "7"}}},
	}, query.Upsert)
	require.NoError(t, err)
	var ids []any
	cur, err := store.db.Collection(store.collection).Find(ctx, bson.M{})
	require.NoError(t, err)
	for cur.Next(ctx) {
		ids = append(ids, cur.Current.Lookup("_id").String())
	}
	require.ElementsMatch(t, []any{`{"$numberInt":"7"}`, `"8"`}, ids)
}

// TestNativeScanStopsOnACancelledContextIntegration proves that the find call
// gets the context of the caller, so a cancelled context fails the find.
func TestNativeScanStopsOnACancelledContextIntegration(t *testing.T) {
	store := openIntegration(t, "native_cancel_find")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := store.NativeScan(ctx, func([]query.Record) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "mongodb find: ")
}

// TestNativeScanStopsWhenTheContextIsCancelledIntegration cancels the context
// after the first page. The scan must stop with the cancel error.
func TestNativeScanStopsWhenTheContextIsCancelledIntegration(t *testing.T) {
	store := openIntegration(t, "native_cancel_pages")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, store.Clear(ctx))
	docs := make([]any, 25)
	for i := range docs {
		docs[i] = bson.D{{Key: "_id", Value: fmt.Sprintf("k%02d", i)}}
	}
	_, err := store.db.Collection(store.collection).InsertMany(ctx, docs)
	require.NoError(t, err)
	store.pageSize = 10

	scanCtx, stop := context.WithCancel(ctx)
	defer stop()
	calls := 0
	err = store.NativeScan(scanCtx, func([]query.Record) error {
		calls++
		stop()
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
}

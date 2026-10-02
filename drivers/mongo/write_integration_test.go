package mongo

import (
	"context"
	"maps"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/zsltg/iq/internal/query"
)

func TestPutUpsertIntegration(t *testing.T) {
	store := openIntegration(t, "put_upsert")
	seedDocs(t, store, nil)
	ctx := context.Background()

	// First write: two new documents.
	stat, err := store.Put(ctx, []query.Record{
		{Key: "a", Value: map[string]any{"n": 1}},
		{Key: "b", Value: map[string]any{"n": 2}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 2, stat.Written)
	require.Equal(t, 0, stat.Overwritten)

	// Re-write one existing and one new: one overwrite, one insert.
	stat, err = store.Put(ctx, []query.Record{
		{Key: "a", Value: map[string]any{"n": 10}},
		{Key: "c", Value: map[string]any{"n": 3}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Written)
	require.Equal(t, 1, stat.Overwritten)

	got, err := store.Get(ctx, []string{"a"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"_id": "a", "n": 10}, got["a"])
}

func TestPutUpsertMixedKeyedAndKeylessIntegration(t *testing.T) {
	store := openIntegration(t, "put_mixed")
	seedDocs(t, store, nil)
	ctx := context.Background()

	// A keyed record is upserted by _id; a keyless record (foreign JSON) is inserted
	// with a minted _id. Both count as written, so the total sums the two counts.
	stat, err := store.Put(ctx, []query.Record{
		{Key: "keyed", Value: map[string]any{"n": 1}},
		{Value: map[string]any{"n": 2}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 2, stat.Written)
	require.Equal(t, 0, stat.Overwritten)

	n, err := store.EstimateCount(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), n)
}

func TestPutInsertOnlySkipsExisting(t *testing.T) {
	store := openIntegration(t, "put_insertonly")
	seedDocs(t, store, nil)
	ctx := context.Background()

	_, err := store.Put(ctx, []query.Record{{Key: "a", Value: map[string]any{"n": 1}}}, query.Upsert)
	require.NoError(t, err)

	stat, err := store.Put(ctx, []query.Record{
		{Key: "a", Value: map[string]any{"n": 99}},
		{Key: "b", Value: map[string]any{"n": 2}},
	}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Written)
	require.Equal(t, 1, stat.Skipped)

	// The existing document was not clobbered.
	got, err := store.Get(ctx, []string{"a", "b"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"_id": "a", "n": 1}, got["a"])
	// The insert is unordered, so the collision on "a" does not stop "b" from
	// landing — the reported write actually happened.
	require.Equal(t, map[string]any{"_id": "b", "n": 2}, got["b"])
}

func TestClearAndDropIntegration(t *testing.T) {
	store := openIntegration(t, "clear_drop")
	seedDocs(t, store, []any{map[string]any{"_id": "a", "n": 1}})
	ctx := context.Background()

	require.NoError(t, store.Clear(ctx))
	n, err := store.EstimateCount(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), n)

	// Drop on an empty/existing collection is a no-op-safe removal.
	require.NoError(t, store.Drop(ctx))
}

func TestDeleteIntegration(t *testing.T) {
	store := openIntegration(t, "delete_docs")
	seedDocs(t, store, nil)
	ctx := context.Background()

	_, err := store.Put(ctx, []query.Record{
		{Key: "d1", Value: map[string]any{"n": 1}},
		{Key: "d2", Value: map[string]any{"n": 2}},
		{Key: "keep", Value: map[string]any{"n": 3}},
	}, query.Upsert)
	require.NoError(t, err)

	// Two present keys delete, one absent key is missing; the counts sum to the
	// request size because DeletedCount is exact.
	stat, err := store.Delete(ctx, []string{"d1", "d2", "absent"})
	require.NoError(t, err)
	require.Equal(t, 2, stat.Deleted)
	require.Equal(t, 1, stat.Missing)

	got, err := store.Get(ctx, []string{"d1", "d2", "keep"})
	require.NoError(t, err)
	require.Nil(t, got["d1"], "deleted key reads as null")
	require.Nil(t, got["d2"], "deleted key reads as null")
	require.Equal(t, map[string]any{"_id": "keep", "n": 3}, got["keep"], "untouched key survives")
}

func TestDeleteEmptyIsNoOpIntegration(t *testing.T) {
	store := openIntegration(t, "delete_empty")

	stat, err := store.Delete(context.Background(), nil)

	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{}, stat, "an empty delete is a zero-stat no-op")
}

func TestTypedScanRoundTripIntegration(t *testing.T) {
	store := openIntegration(t, "typed_scan")
	seedDocs(t, store, nil)
	ctx := context.Background()

	_, err := store.Put(ctx, []query.Record{
		{Key: "a", Value: map[string]any{"n": 1}},
		{Key: "b", Value: map[string]any{"n": 2}},
	}, query.Upsert)
	require.NoError(t, err)

	var got []query.Record
	require.NoError(t, store.TypedScan(ctx, func(batch []query.Record) error {
		got = append(got, batch...)
		return nil
	}))
	sort.Slice(got, func(i, j int) bool { return got[i].Key < got[j].Key })
	require.Len(t, got, 2)
	require.Equal(t, "a", got[0].Key)
	require.Equal(t, "document", got[0].Type)
	require.Equal(t, map[string]any{"_id": "a", "n": 1}, got[0].Value)
}

func TestPutEmptyBatchIsNoOp(t *testing.T) {
	store := openIntegration(t, "put_empty")
	seedDocs(t, store, nil)

	stat, err := store.Put(context.Background(), nil, query.Upsert)

	require.NoError(t, err, "an empty batch is a no-op, not an empty bulk write")
	require.Equal(t, query.WriteStat{}, stat)
}

// TestPutRejectsNonObjectValue proves both write modes reject a scalar value
// before any write.
func TestPutRejectsNonObjectValue(t *testing.T) {
	store := openIntegration(t, "put_scalar")
	seedDocs(t, store, nil)

	tests := []struct {
		name string
		mode query.WriteMode
	}{
		{"upsert", query.Upsert},
		{"insert only", query.InsertOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := store.Put(context.Background(), []query.Record{
				{Key: "a", Value: "scalar"},
			}, tt.mode)

			require.ErrorContains(t, err, "is not a JSON object", "the value is rejected before any write")
			n, err := store.EstimateCount(context.Background())
			require.NoError(t, err)
			require.Zero(t, n, "nothing was written")
		})
	}
}

// TestPutUpsertKeylessFirstMintsObjectID pins the keyless branch: a keyless record
// is inserted with a MongoDB-minted ObjectID (never replaced under an empty _id),
// and the records after it in the batch are still written.
func TestPutUpsertKeylessFirstMintsObjectID(t *testing.T) {
	store := openIntegration(t, "put_keyless_first")
	seedDocs(t, store, nil)
	ctx := context.Background()

	stat, err := store.Put(ctx, []query.Record{
		{Value: map[string]any{"n": 1}},
		{Key: "keyed", Value: map[string]any{"n": 2}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 2, stat.Written)

	keys := map[string]any{}
	require.NoError(t, store.ScanBatches(ctx, func(batch map[string]any) error {
		maps.Copy(keys, batch)
		return nil
	}))
	require.Len(t, keys, 2, "the record after the keyless one was written too")
	require.Contains(t, keys, "keyed")
	delete(keys, "keyed")
	for k := range keys {
		_, err := bson.ObjectIDFromHex(k)
		require.NoError(t, err, "the keyless record got a minted ObjectID _id, not an empty one")
	}
}

// TestPutUpsertIsUnordered proves the bulk write is unordered: a document the
// server rejects does not stop the rest of the batch from being written.
func TestPutUpsertIsUnordered(t *testing.T) {
	store := openIntegration(t, "put_unordered")
	seedDocs(t, store, nil)
	ctx := context.Background()

	_, err := store.db.Collection(store.collection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "u", Value: int32(1)}},
		Options: options.Index().SetUnique(true),
	})
	require.NoError(t, err)

	_, err = store.Put(ctx, []query.Record{{Key: "a", Value: map[string]any{"u": 1}}}, query.Upsert)
	require.NoError(t, err)

	// "dup" collides with "a" on the unique index; "c" is valid and must still land.
	_, err = store.Put(ctx, []query.Record{
		{Key: "dup", Value: map[string]any{"u": 1}},
		{Key: "c", Value: map[string]any{"u": 2}},
	}, query.Upsert)
	require.Error(t, err, "the rejected document fails the batch")

	got, err := store.Get(ctx, []string{"c", "dup"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"_id": "c", "u": 2}, got["c"], "an unordered write continues past the rejection")
	require.NotContains(t, got, "dup", "the rejected document was not written")
}

// TestDeleteChunksAccumulateCounts pins the per-chunk accounting: with more chunks
// than one, the deleted and missing counts must sum across them.
func TestDeleteChunksAccumulateCounts(t *testing.T) {
	store := openIntegration(t, "delete_chunks")
	seedDocs(t, store, nil)
	ctx := context.Background()

	_, err := store.Put(ctx, []query.Record{
		{Key: "d1", Value: map[string]any{"n": 1}},
		{Key: "d2", Value: map[string]any{"n": 2}},
	}, query.Upsert)
	require.NoError(t, err)
	// One key per round trip, so the three keys below span three chunks: present,
	// absent, present.
	store.pageSize = 1

	stat, err := store.Delete(ctx, []string{"d1", "absent", "d2"})

	require.NoError(t, err)
	require.Equal(t, 2, stat.Deleted, "both chunks that deleted a document are counted")
	require.Equal(t, 1, stat.Missing, "the middle chunk's miss is counted")
}

package mongo

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

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
	got, err := store.Get(ctx, []string{"a"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"_id": "a", "n": 1}, got["a"])
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

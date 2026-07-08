package couchdb

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestPutUpsert(t *testing.T) {
	st := seedDB(t)
	ctx := skipShort(t)

	stat, err := st.Put(ctx, []query.Record{
		{Key: "1", Value: map[string]any{"title": "A"}},
		{Key: "2", Value: map[string]any{"title": "B"}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 2, stat.Written)
	require.Equal(t, 0, stat.Overwritten)

	// A second upsert of an existing key overwrites it (fetching its current _rev).
	stat, err = st.Put(ctx, []query.Record{{Key: "1", Value: map[string]any{"title": "A2"}}}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Overwritten)
	require.Equal(t, 0, stat.Written)

	got, err := st.Get(ctx, []string{"1"})
	require.NoError(t, err)
	require.Equal(t, "A2", got["1"].(map[string]any)["title"])
}

func TestPutInsertOnlySkipsConflicts(t *testing.T) {
	st := seedDB(t, map[string]any{"_id": "1", "title": "orig"})
	ctx := skipShort(t)

	stat, err := st.Put(ctx, []query.Record{
		{Key: "1", Value: map[string]any{"title": "new"}},
		{Key: "2", Value: map[string]any{"title": "two"}},
	}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Written)
	require.Equal(t, 1, stat.Skipped)

	got, err := st.Get(ctx, []string{"1"})
	require.NoError(t, err)
	require.Equal(t, "orig", got["1"].(map[string]any)["title"], "an existing key is not clobbered")
}

func TestPutKeylessMintsIDButRejectsScalar(t *testing.T) {
	st := seedDB(t)
	ctx := skipShort(t)

	// A keyless object still mints its own _id and is written.
	stat, err := st.Put(ctx, []query.Record{
		{Key: "", Value: map[string]any{"title": "minted-id"}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Written)

	// A scalar value is rejected rather than wrapped as {value: ...}, so a copy
	// round-trips exactly. Documents are built before the bulk call, so a good record
	// batched ahead of the scalar is not written either — the whole batch fails first.
	_, err = st.Put(ctx, []query.Record{
		{Key: "good", Value: map[string]any{"title": "ok"}},
		{Key: "k", Value: "bare-scalar"},
	}, query.Upsert)
	require.ErrorContains(t, err, "is not a JSON object")

	got, err := st.Get(ctx, []string{"good", "k"})
	require.NoError(t, err)
	require.Nil(t, got["k"], "the rejected scalar was never written")
	require.Nil(t, got["good"], "the batch failed before any document was written")
}

func TestPutStripsIdentityFields(t *testing.T) {
	st := seedDB(t)
	ctx := skipShort(t)

	// A record whose value still carries a foreign _id/_rev must take its identity
	// from the record key, not the embedded fields.
	_, err := st.Put(ctx, []query.Record{
		{Key: "1", Value: map[string]any{"_id": "foreign", "_rev": "9-bad", "title": "T"}},
	}, query.Upsert)
	require.NoError(t, err)

	got, err := st.Get(ctx, []string{"1"})
	require.NoError(t, err)
	require.Equal(t, "1", got["1"].(map[string]any)["_id"])
}

func TestDeleteIntegration(t *testing.T) {
	st := seedDB(t)
	ctx := skipShort(t)

	_, err := st.Put(ctx, []query.Record{
		{Key: "d1", Value: map[string]any{"title": "one"}},
		{Key: "d2", Value: map[string]any{"title": "two"}},
		{Key: "keep", Value: map[string]any{"title": "survives"}},
	}, query.Upsert)
	require.NoError(t, err)

	// Two live keys are tombstoned; the absent key is counted Missing, not errored.
	stat, err := st.Delete(ctx, []string{"d1", "d2", "absent"})
	require.NoError(t, err)
	require.Equal(t, 2, stat.Deleted)
	require.Equal(t, 1, stat.Missing)

	got, err := st.Get(ctx, []string{"d1", "d2", "keep"})
	require.NoError(t, err)
	require.Nil(t, got["d1"], "the tombstoned document reads as null")
	require.Nil(t, got["d2"], "the tombstoned document reads as null")
	require.Equal(t, "survives", got["keep"].(map[string]any)["title"], "an untouched document survives")
}

func TestDeleteEmptyIsNoOpIntegration(t *testing.T) {
	st := seedDB(t)
	ctx := skipShort(t)

	stat, err := st.Delete(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{}, stat, "an empty delete touches nothing")
}

func TestClearKeepsDesignDocs(t *testing.T) {
	docs := append(booksDocs(), map[string]any{"_id": "_design/idx", "language": "query"})
	st := seedDB(t, docs...)
	ctx := skipShort(t)

	require.NoError(t, st.Clear(ctx))

	got := collect(t, func(fn func(map[string]any) error) error { return st.ScanBatches(ctx, fn) })
	require.Empty(t, got, "all data documents are deleted")

	// The design document (an index) survives a clear.
	err := adminClient(t).DB(dbName(t)).Get(ctx, "_design/idx").Err()
	require.NoError(t, err)
}

func TestDrop(t *testing.T) {
	st := seedDB(t, booksDocs()...)
	ctx := skipShort(t)
	name := dbName(t)

	require.NoError(t, st.Drop(ctx))

	exists, err := adminClient(t).DBExists(ctx, name)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestTypedScan(t *testing.T) {
	st := seedDB(t, booksDocs()...)
	ctx := skipShort(t)

	var recs []query.Record
	require.NoError(t, st.TypedScan(ctx, func(batch []query.Record) error {
		recs = append(recs, batch...)
		return nil
	}))
	require.Len(t, recs, 4)

	keys := make([]string, len(recs))
	for i, r := range recs {
		require.Equal(t, "document", r.Type)
		keys[i] = r.Key
	}
	sort.Strings(keys)
	require.Equal(t, []string{"1", "2", "3", "4"}, keys)
}

func TestWriteNoDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping couchdb integration test in -short mode")
	}
	st := openIntegration(t, "")
	ctx := skipShort(t)

	_, err := st.Put(ctx, []query.Record{{Key: "1", Value: map[string]any{}}}, query.Upsert)
	require.ErrorIs(t, err, errNoDatabase)
	require.ErrorIs(t, st.Clear(ctx), errNoDatabase)
	require.ErrorIs(t, st.Drop(context.Background()), errNoDatabase)
}

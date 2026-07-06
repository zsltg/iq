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

func TestPutKeylessAndScalar(t *testing.T) {
	st := seedDB(t)
	ctx := skipShort(t)

	stat, err := st.Put(ctx, []query.Record{
		{Key: "", Value: map[string]any{"title": "minted-id"}},
		{Key: "k", Value: "bare-scalar"},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 2, stat.Written)

	// The scalar value is wrapped so it is a valid document.
	got, err := st.Get(ctx, []string{"k"})
	require.NoError(t, err)
	require.Equal(t, "bare-scalar", got["k"].(map[string]any)["value"])
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

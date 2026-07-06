package cassandra

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestPutUpsertIntegration(t *testing.T) {
	st := seedTable(t, "books", "CREATE TABLE books (id int PRIMARY KEY, title text)")

	stat, err := st.Put(context.Background(), []query.Record{
		{Key: "1", Type: "row", Value: map[string]any{"id": 1, "title": "A"}},
		{Key: "2", Type: "row", Value: map[string]any{"id": 2, "title": "B"}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 2}, stat)

	// A re-upsert of an existing key overwrites; Cassandra counts it as Written.
	stat, err = st.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"id": 1, "title": "A2"}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1}, stat)

	got, err := st.Get(context.Background(), []string{"1"})
	require.NoError(t, err)
	require.Equal(t, "A2", got["1"].(map[string]any)["title"])
}

func TestPutInsertOnlySkipsExisting(t *testing.T) {
	st := seedTable(t, "books", "CREATE TABLE books (id int PRIMARY KEY, title text)")

	_, err := st.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"id": 1, "title": "original"}},
	}, query.Upsert)
	require.NoError(t, err)

	stat, err := st.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"id": 1, "title": "clobber"}},
		{Key: "2", Value: map[string]any{"id": 2, "title": "fresh"}},
	}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1, Skipped: 1}, stat)

	got, err := st.Get(context.Background(), []string{"1"})
	require.NoError(t, err)
	require.Equal(t, "original", got["1"].(map[string]any)["title"], "an existing key must not be clobbered")
}

func TestPutKeyIsAuthoritativePrimaryKey(t *testing.T) {
	st := seedTable(t, "books", "CREATE TABLE books (id int PRIMARY KEY, title text)")

	// The value object omits the primary key; the record key must supply it, so the
	// write succeeds and the row is keyed by 7 (not a missing-column error).
	stat, err := st.Put(context.Background(), []query.Record{
		{Key: "7", Value: map[string]any{"title": "KeyOnly"}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1}, stat)

	got, err := st.Get(context.Background(), []string{"7"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"id": 7, "title": "KeyOnly"}, got["7"])
}

func TestPutCollections(t *testing.T) {
	st := seedTable(t, "tagged",
		"CREATE TABLE tagged (id int PRIMARY KEY, tags set<text>, meta map<text, int>)")

	stat, err := st.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{
			"id":   1,
			"tags": []any{"x", "y"},
			"meta": map[string]any{"a": 1, "b": 2},
		}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1}, stat)

	got, err := st.Get(context.Background(), []string{"1"})
	require.NoError(t, err)
	row := got["1"].(map[string]any)
	require.ElementsMatch(t, []any{"x", "y"}, row["tags"])
	require.Equal(t, map[string]any{"a": 1, "b": 2}, row["meta"])
}

func TestClearAndDropIntegration(t *testing.T) {
	st := seedTable(t, "books",
		"CREATE TABLE books (id int PRIMARY KEY, title text)",
		"INSERT INTO books (id, title) VALUES (1, 'Hobbit')")

	require.NoError(t, st.Clear(context.Background()))
	got := map[string]any{}
	require.NoError(t, st.ScanBatches(context.Background(), func(batch map[string]any) error {
		for k, v := range batch {
			got[k] = v
		}
		return nil
	}))
	require.Empty(t, got, "clear must empty the table")

	require.NoError(t, st.Drop(context.Background()))
	// After a drop the table is gone, so opening it fails.
	_, err := Open(context.Background(), testURL(), "books", nil, 0)
	require.Error(t, err)
}

func TestTypedScanRoundTripIntegration(t *testing.T) {
	src := seedTable(t, "src_books",
		"CREATE TABLE src_books (id int PRIMARY KEY, title text, author text)",
		"INSERT INTO src_books (id, title, author) VALUES (1, 'Hobbit', 'Tolkien')",
		"INSERT INTO src_books (id, title, author) VALUES (2, 'Dune', 'Herbert')")
	dst := seedTable(t, "dst_books",
		"CREATE TABLE dst_books (id int PRIMARY KEY, title text, author text)")

	var recs []query.Record
	require.NoError(t, src.TypedScan(context.Background(), func(batch []query.Record) error {
		recs = append(recs, batch...)
		return nil
	}))
	require.Len(t, recs, 2)
	for _, r := range recs {
		require.Equal(t, "row", r.Type)
	}

	stat, err := dst.Put(context.Background(), recs, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 2}, stat)

	want, err := src.Get(context.Background(), []string{"1", "2"})
	require.NoError(t, err)
	got, err := dst.Get(context.Background(), []string{"1", "2"})
	require.NoError(t, err)
	require.Equal(t, want, got)
}

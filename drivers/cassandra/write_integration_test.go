package cassandra

import (
	"context"
	"maps"
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

	// A mixed re-upsert: key 1 exists (Overwritten), key 3 is new (Written), and a
	// keyless record carries its key in the value and cannot be pre-read (Written).
	stat, err = st.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"id": 1, "title": "A2"}},
		{Key: "3", Value: map[string]any{"id": 3, "title": "C"}},
		{Value: map[string]any{"id": 4, "title": "D"}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 2, Overwritten: 1}, stat)

	got, err := st.Get(context.Background(), []string{"1"})
	require.NoError(t, err)
	require.Equal(t, "A2", got["1"].(map[string]any)["title"])
}

func TestPutUpsertCountsCompositeOverwrite(t *testing.T) {
	st := seedTable(t, "events",
		"CREATE TABLE events (day text, id int, note text, PRIMARY KEY (day, id))")

	stat, err := st.Put(context.Background(), []query.Record{
		{Key: `["mon","1"]`, Value: map[string]any{"day": "mon", "id": 1, "note": "a"}},
		{Key: `["mon","2"]`, Value: map[string]any{"day": "mon", "id": 2, "note": "b"}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 2}, stat)

	// One composite key exists (Overwritten via a per-key point pre-read), one is new.
	stat, err = st.Put(context.Background(), []query.Record{
		{Key: `["mon","1"]`, Value: map[string]any{"day": "mon", "id": 1, "note": "a2"}},
		{Key: `["tue","1"]`, Value: map[string]any{"day": "tue", "id": 1, "note": "c"}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1, Overwritten: 1}, stat)
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
		maps.Copy(got, batch)
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

func TestDeleteIntegration(t *testing.T) {
	st := seedTable(t, "books", "CREATE TABLE books (id int PRIMARY KEY, title text)")

	_, err := st.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"id": 1, "title": "Hobbit"}},
		{Key: "2", Value: map[string]any{"id": 2, "title": "Dune"}},
		{Key: "3", Value: map[string]any{"id": 3, "title": "Neuromancer"}},
	}, query.Upsert)
	require.NoError(t, err)

	// A mixed delete: keys 1 and 2 exist (Deleted), key 9 was never written (Missing).
	// The DELETE runs for every requested key regardless, so Deleted+Missing==len(keys).
	stat, err := st.Delete(context.Background(), []string{"1", "2", "9"})
	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Deleted: 2, Missing: 1}, stat)

	got, err := st.Get(context.Background(), []string{"1", "2", "3"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"3": map[string]any{"id": 3, "title": "Neuromancer"},
	}, got, "the deleted rows are gone, the un-named row survives")
}

func TestDeleteCompositeKeyIntegration(t *testing.T) {
	st := seedTable(t, "sales",
		"CREATE TABLE sales (country text, id int, amount int, PRIMARY KEY (country, id))")

	_, err := st.Put(context.Background(), []query.Record{
		{Key: `["US","1"]`, Value: map[string]any{"country": "US", "id": 1, "amount": 100}},
	}, query.Upsert)
	require.NoError(t, err)

	// A composite key is deleted by its JSON-array spelling: it exists, so Deleted==1.
	stat, err := st.Delete(context.Background(), []string{`["US","1"]`})
	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Deleted: 1}, stat)

	got, err := st.Get(context.Background(), []string{`["US","1"]`})
	require.NoError(t, err)
	require.Empty(t, got, "the composite row is gone")

	// An absent composite key counts as Missing, never an error (delete is idempotent).
	stat, err = st.Delete(context.Background(), []string{`["US","9"]`})
	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Missing: 1}, stat)
}

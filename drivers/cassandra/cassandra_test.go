package cassandra

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
)

func TestGetSingleKey(t *testing.T) {
	st := seedTable(t, "books",
		"CREATE TABLE books (id int PRIMARY KEY, title text, author text)",
		"INSERT INTO books (id, title, author) VALUES (1, 'Hobbit', 'Tolkien')",
		"INSERT INTO books (id, title, author) VALUES (2, 'Dune', 'Herbert')")

	got, err := st.Get(context.Background(), []string{"1", "3"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"1": map[string]any{"id": 1, "title": "Hobbit", "author": "Tolkien"},
	}, got)
	require.NotContains(t, got, "3", "a key with no row is absent, not a nil entry")
}

func TestGetCompositeKey(t *testing.T) {
	st := seedTable(t, "sales",
		"CREATE TABLE sales (country text, id int, amount int, PRIMARY KEY (country, id))",
		"INSERT INTO sales (country, id, amount) VALUES ('US', 1, 100)",
		"INSERT INTO sales (country, id, amount) VALUES ('US', 2, 200)",
		// A third row shares the partition key but is NOT requested, so a lookup that
		// keyed on the partition alone (not the full primary key) would wrongly include it.
		"INSERT INTO sales (country, id, amount) VALUES ('US', 3, 300)")

	// Request two composite keys plus a missing one: each is fetched with its own
	// full-primary-key point query, so exactly those present rows come back — never
	// the sibling US/3, and not just the first.
	got, err := st.Get(context.Background(), []string{`["US","1"]`, `["US","2"]`, `["US","9"]`})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		`["US","1"]`: map[string]any{"country": "US", "id": 1, "amount": 100},
		`["US","2"]`: map[string]any{"country": "US", "id": 2, "amount": 200},
	}, got)
	require.NotContains(t, got, `["US","9"]`, "a key with no row is absent, not a nil entry")
}

func TestScanBatchesYieldsAllRows(t *testing.T) {
	// Assert the exact page-size sequence for a full-page-multiple dataset (no empty
	// trailing page) and for one with a leftover row, so the flush-at-pageSize and
	// final-partial-flush boundaries are both pinned.
	tests := []struct {
		name      string
		rows      int
		pageSize  int
		wantPages []int
	}{
		{name: "even fills whole pages", rows: 4, pageSize: 2, wantPages: []int{2, 2}},
		{name: "odd leaves a partial page", rows: 5, pageSize: 2, wantPages: []int{2, 2, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inserts := make([]string, tt.rows)
			for i := range inserts {
				inserts[i] = fmt.Sprintf("INSERT INTO nums (id, v) VALUES (%d, 'v%d')", i+1, i+1)
			}
			st := seedTable(t, "nums", "CREATE TABLE nums (id int PRIMARY KEY, v text)", inserts...)
			st.pageSize = tt.pageSize

			var keys []string
			var pageSizes []int
			err := st.ScanBatches(context.Background(), func(batch map[string]any) error {
				pageSizes = append(pageSizes, len(batch))
				for k := range batch {
					keys = append(keys, k)
				}
				return nil
			})
			require.NoError(t, err)
			require.Equal(t, tt.wantPages, pageSizes, "exact page-size sequence")
			want := make([]string, tt.rows)
			for i := range want {
				want[i] = strconv.Itoa(i + 1)
			}
			require.ElementsMatch(t, want, keys)
		})
	}
}

func TestScanFilteredReturnsMatching(t *testing.T) {
	st := seedTable(t, "books",
		"CREATE TABLE books (id int PRIMARY KEY, author text)",
		"INSERT INTO books (id, author) VALUES (1, 'Tolkien')",
		"INSERT INTO books (id, author) VALUES (2, 'Herbert')",
		"INSERT INTO books (id, author) VALUES (3, 'Tolkien')")

	where, args, ok := toCQL(columnTypes(st.meta), eqAuthor("Tolkien"))
	require.True(t, ok)
	require.Equal(t, `"author" = ?`, where)
	require.Len(t, args, 1)

	got := map[string]any{}
	err := st.ScanFiltered(context.Background(), eqAuthor("Tolkien"), func(batch map[string]any) error {
		for k, v := range batch {
			got[k] = v
		}
		return nil
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"1", "3"}, keysOf(got))
}

func TestScanNormalizesCollections(t *testing.T) {
	st := seedTable(t, "tagged",
		"CREATE TABLE tagged (id int PRIMARY KEY, tags set<text>)",
		"INSERT INTO tagged (id, tags) VALUES (1, {'a', 'b', 'c'})")

	got, err := st.Get(context.Background(), []string{"1"})
	require.NoError(t, err)
	row := got["1"].(map[string]any)
	require.ElementsMatch(t, []any{"a", "b", "c"}, row["tags"])
}

func TestQueryRawCQL(t *testing.T) {
	st := seedTable(t, "books",
		"CREATE TABLE books (id int PRIMARY KEY, title text)",
		"INSERT INTO books (id, title) VALUES (1, 'Hobbit')")

	res, err := st.Query(context.Background(), []string{"SELECT id, title FROM books WHERE id = 1"})
	require.NoError(t, err)
	rows, ok := res.([]any)
	require.True(t, ok)
	require.Len(t, rows, 1)
	require.Equal(t, map[string]any{"id": 1, "title": "Hobbit"}, rows[0])
}

func TestGetNoTable(t *testing.T) {
	st := schemaStore(t)
	_, err := st.Get(context.Background(), []string{"1"})
	require.ErrorIs(t, err, errNoTable)
}

// eqAuthor builds the pushdown predicate `.author == v` for the filter tests.
func eqAuthor(v string) predicate.Node {
	return predicate.Eq{Path: []string{"author"}, Value: v}
}

// keysOf returns the keys of a batch map, for order-insensitive assertions.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

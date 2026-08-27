package hbase

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tsuna/gohbase/pb"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// cf builds a one-cell family map for seeding the fake.
func cell(family, qualifier, value string) map[string]map[string][]byte {
	return map[string]map[string][]byte{family: {qualifier: []byte(value)}}
}

func TestGetReturnsRowsAndOmitsMissing(t *testing.T) {
	fc := newFakeClient()
	fc.seed("1", cell("cf", "title", "Dune"))
	fc.seed("2", cell("cf", "title", "Hyperion"))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	got, err := st.Get(context.Background(), []string{"1", "2", "missing"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"1": map[string]any{"cf": map[string]any{"title": "Dune"}},
		"2": map[string]any{"cf": map[string]any{"title": "Hyperion"}},
	}, got)
}

func TestGetEmptyKeysNoRoundTrip(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	got, err := st.Get(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestGetNoTableErrors(t *testing.T) {
	st := newFakeStore(newFakeClient(), &fakeAdmin{}, "", typeMap{}, ctAuto)
	_, err := st.Get(context.Background(), []string{"1"})
	require.ErrorIs(t, err, errNoTable)
}

func TestGetPropagatesError(t *testing.T) {
	fc := newFakeClient()
	fc.getErr = errors.New("boom")
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	_, err := st.Get(context.Background(), []string{"1"})
	require.ErrorContains(t, err, "hbase get")
}

func TestScanBatchesPagesEveryRow(t *testing.T) {
	fc := newFakeClient()
	for _, k := range []string{"1", "2", "3", "4", "5"} {
		fc.seed(k, cell("cf", "n", k))
	}
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto) // pageSize 2

	var pages []int
	seen := map[string]any{}
	err := st.ScanBatches(context.Background(), func(batch map[string]any) error {
		pages = append(pages, len(batch))
		maps.Copy(seen, batch)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, seen, 5)
	require.Equal(t, []int{2, 2, 1}, pages) // pageSize 2 over 5 rows
}

func TestScanBatchesExactMultipleNoTrailingEmptyPage(t *testing.T) {
	fc := newFakeClient()
	for _, k := range []string{"1", "2", "3", "4"} {
		fc.seed(k, cell("cf", "n", k))
	}
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto) // pageSize 2

	var pages []int
	err := st.ScanBatches(context.Background(), func(batch map[string]any) error {
		pages = append(pages, len(batch))
		return nil
	})
	require.NoError(t, err)
	// 4 rows / pageSize 2 = two full pages, and no trailing empty page.
	require.Equal(t, []int{2, 2}, pages)
}

func TestScanBatchesNoTableErrors(t *testing.T) {
	st := newFakeStore(newFakeClient(), &fakeAdmin{}, "", typeMap{}, ctAuto)
	err := st.ScanBatches(context.Background(), func(map[string]any) error { return nil })
	require.ErrorIs(t, err, errNoTable)
}

func TestScanBatchesStopsOnFnError(t *testing.T) {
	fc := newFakeClient()
	for _, k := range []string{"1", "2", "3", "4"} {
		fc.seed(k, cell("cf", "n", k))
	}
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	sentinel := errors.New("stop")
	err := st.ScanBatches(context.Background(), func(map[string]any) error { return sentinel })
	require.ErrorIs(t, err, sentinel)
}

func TestScanFilteredStreamsRows(t *testing.T) {
	fc := newFakeClient()
	fc.seed("1", cell("cf", "author", "Herbert"))
	fc.seed("2", cell("cf", "author", "Simmons"))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	seen := map[string]any{}
	err := st.ScanFiltered(context.Background(),
		predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"},
		func(batch map[string]any) error {
			maps.Copy(seen, batch)
			return nil
		})
	require.NoError(t, err)
	// The fake ignores the server-side filter; the plumbing yields every row and the
	// engine (not the Store) re-runs the jq. Assert the rows decode and stream.
	require.Len(t, seen, 2)
}

func TestQueryGet(t *testing.T) {
	fc := newFakeClient()
	fc.seed("42", cell("cf", "title", "Dune"))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	got, err := st.Query(context.Background(), []string{"get", "books", "42"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"cf": map[string]any{"title": "Dune"}}, got)

	missing, err := st.Query(context.Background(), []string{"get", "books", "nope"})
	require.NoError(t, err)
	require.Nil(t, missing)
}

func TestQueryScanWithLimit(t *testing.T) {
	fc := newFakeClient()
	for _, k := range []string{"1", "2", "3"} {
		fc.seed(k, cell("cf", "n", k))
	}
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	got, err := st.Query(context.Background(), []string{"scan", "books", "2"})
	require.NoError(t, err)
	rows, ok := got.(map[string]any)
	require.True(t, ok)
	require.Len(t, rows, 2)
}

func TestQueryScanNoLimitReturnsAll(t *testing.T) {
	fc := newFakeClient()
	for _, k := range []string{"1", "2", "3"} {
		fc.seed(k, cell("cf", "n", k))
	}
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	// scan with no limit argument must return every row, not stop early.
	got, err := st.Query(context.Background(), []string{"scan", "books"})
	require.NoError(t, err)
	require.Len(t, got.(map[string]any), 3)
}

func TestQueryCount(t *testing.T) {
	fc := newFakeClient()
	for _, k := range []string{"1", "2", "3"} {
		fc.seed(k, cell("cf", "n", k))
	}
	// A row with no cells must not be counted (a defensive guard against an empty
	// scan result).
	fc.seed("ghost", map[string]map[string][]byte{})
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	got, err := st.Query(context.Background(), []string{"count", "books"})
	require.NoError(t, err)
	require.Equal(t, 3, got)
}

func TestQueryDeleteColumn(t *testing.T) {
	fc := newFakeClient()
	fc.seed("7", map[string]map[string][]byte{"cf": {"title": []byte("Sabriel"), "author": []byte("Nix")}})
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	// delete with a family:qualifier removes only that cell, keeping the row.
	_, err := st.Query(context.Background(), []string{"delete", "books", "7", "cf:title"})
	require.NoError(t, err)
	require.NotContains(t, fc.rows["7"]["cf"], "title")
	require.Contains(t, fc.rows["7"]["cf"], "author")
}

func TestQueryPutAndDelete(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	_, err := st.Query(context.Background(), []string{"put", "books", "7", "cf:title", "Sabriel"})
	require.NoError(t, err)
	require.Equal(t, []byte("Sabriel"), fc.rows["7"]["cf"]["title"])

	_, err = st.Query(context.Background(), []string{"delete", "books", "7"})
	require.NoError(t, err)
	require.NotContains(t, fc.rows, "7")
}

func TestQueryErrors(t *testing.T) {
	st := newFakeStore(newFakeClient(), &fakeAdmin{}, "books", typeMap{}, ctAuto)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"empty", nil, "empty command"},
		{"unknown verb", []string{"scanx", "books"}, "unknown command"},
		{"get arity", []string{"get", "books"}, "get needs"},
		{"scan arity", []string{"scan"}, "scan needs"},
		{"put arity", []string{"put", "books", "7", "cf:title"}, "put needs"},
		{"put bad column", []string{"put", "books", "7", "title", "x"}, "must be family:qualifier"},
		{"delete arity", []string{"delete"}, "delete needs"},
		{"delete bad column", []string{"delete", "books", "7", "title"}, "must be family:qualifier"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := st.Query(context.Background(), tt.args)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestPutUpsert(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	batch := []query.Record{
		{Key: "1", Type: "row", Value: map[string]any{"cf": map[string]any{"title": "Dune"}}},
		{Key: "2", Type: "row", Value: map[string]any{"cf": map[string]any{"title": "Hyperion"}}},
	}
	stat, err := st.Put(context.Background(), batch, query.Upsert)
	require.NoError(t, err)
	// Neither row existed, so the pre-read counts both as fresh writes.
	require.Equal(t, query.WriteStat{Written: 2}, stat)
	require.Equal(t, []byte("Dune"), fc.rows["1"]["cf"]["title"])
}

func TestPutUpsertCountsOverwrites(t *testing.T) {
	fc := newFakeClient()
	fc.seed("1", cell("cf", "title", "Old"))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	batch := []query.Record{
		{Key: "1", Value: map[string]any{"cf": map[string]any{"title": "New"}}},   // exists → overwrite
		{Key: "2", Value: map[string]any{"cf": map[string]any{"title": "Fresh"}}}, // new → write
	}
	stat, err := st.Put(context.Background(), batch, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1, Overwritten: 1}, stat)
	require.Equal(t, []byte("New"), fc.rows["1"]["cf"]["title"]) // the write still lands
}

func TestPutUpsertPreReadError(t *testing.T) {
	fc := newFakeClient()
	fc.getErr = errors.New("region down")
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	_, err := st.Put(context.Background(),
		[]query.Record{{Key: "1", Value: map[string]any{"cf": map[string]any{"title": "Dune"}}}}, query.Upsert)
	require.ErrorContains(t, err, "hbase exists")
	require.Zero(t, fc.putCalls) // a failed pre-read aborts before any write
}

func TestPutInsertOnlySkipsExisting(t *testing.T) {
	fc := newFakeClient()
	fc.seed("1", cell("cf", "title", "Old"))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	batch := []query.Record{
		{Key: "1", Value: map[string]any{"cf": map[string]any{"title": "New"}}},
		{Key: "2", Value: map[string]any{"cf": map[string]any{"title": "Fresh"}}},
	}
	stat, err := st.Put(context.Background(), batch, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1, Skipped: 1}, stat)
	require.Equal(t, []byte("Old"), fc.rows["1"]["cf"]["title"]) // unchanged
	require.Equal(t, []byte("Fresh"), fc.rows["2"]["cf"]["title"])
}

func TestPutRejectsNonRowValue(t *testing.T) {
	st := newFakeStore(newFakeClient(), &fakeAdmin{}, "books", typeMap{}, ctAuto)
	_, err := st.Put(context.Background(), []query.Record{{Key: "1", Value: "scalar"}}, query.Upsert)
	require.ErrorContains(t, err, "must be a {family: {qualifier: value}} object")
}

func TestColumnsForDropsEmptyFamily(t *testing.T) {
	st := newFakeStore(newFakeClient(), &fakeAdmin{}, "books", typeMap{}, ctAuto)
	// A family with no qualifiers is dropped from the write, not sent as an empty
	// column family; the guard cell comes from the family that does have cells.
	rec := query.Record{Key: "1", Value: map[string]any{
		"empty": map[string]any{},
		"cf":    map[string]any{"title": "Dune"},
	}}
	values, guardFamily, guardQualifier, err := st.columnsFor(rec)
	require.NoError(t, err)
	require.NotContains(t, values, "empty")
	require.Contains(t, values, "cf")
	require.Equal(t, "cf", guardFamily)
	require.Equal(t, "title", guardQualifier)
}

func TestPutRejectsEmptyRow(t *testing.T) {
	st := newFakeStore(newFakeClient(), &fakeAdmin{}, "books", typeMap{}, ctAuto)
	_, err := st.Put(context.Background(), []query.Record{{Key: "1", Value: map[string]any{}}}, query.Upsert)
	require.ErrorContains(t, err, "no cells to write")
}

func TestClearDeletesEveryRow(t *testing.T) {
	fc := newFakeClient()
	for _, k := range []string{"1", "2", "3"} {
		fc.seed(k, cell("cf", "n", k))
	}
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	require.NoError(t, st.Clear(context.Background()))
	require.Empty(t, fc.rows)
	require.Equal(t, 3, fc.delCalls)
}

func TestDropDisablesThenDeletes(t *testing.T) {
	fa := &fakeAdmin{}
	st := newFakeStore(newFakeClient(), fa, "ns:books", typeMap{}, ctAuto)
	require.NoError(t, st.Drop(context.Background()))
	require.Equal(t, []string{"ns:books"}, fa.disabled)
	require.Equal(t, []string{"ns:books"}, fa.deleted)
}

func TestTypedScanTagsRows(t *testing.T) {
	fc := newFakeClient()
	fc.seed("1", cell("cf", "title", "Dune"))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	var recs []query.Record
	err := st.TypedScan(context.Background(), func(batch []query.Record) error {
		recs = append(recs, batch...)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "row", recs[0].Type)
	require.Equal(t, "1", recs[0].Key)
}

func TestInspectTablesListsNamespace(t *testing.T) {
	fa := &fakeAdmin{tables: []*pb.TableName{tableName("default", "books"), tableName("default", "authors")}}
	st := newFakeStore(newFakeClient(), fa, "books", typeMap{}, ctAuto)
	got, err := st.InspectTables(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[string]any{"namespace": "default", "tables": []string{"authors", "books"}}, got)
}

func TestInspectTablesUsesTableNamespace(t *testing.T) {
	// A namespace-qualified table scopes the listing to that namespace, not "default".
	fa := &fakeAdmin{tables: []*pb.TableName{tableName("app", "events")}}
	st := newFakeStore(newFakeClient(), fa, "app:events", typeMap{}, ctAuto)
	got, err := st.InspectTables(context.Background())
	require.NoError(t, err)
	require.Equal(t, "app", got.(map[string]any)["namespace"])
}

func TestInspectTablesDefaultNamespaceWhenNoTable(t *testing.T) {
	fa := &fakeAdmin{tables: []*pb.TableName{tableName("default", "books")}}
	st := newFakeStore(newFakeClient(), fa, "", typeMap{}, ctAuto)
	got, err := st.InspectTables(context.Background())
	require.NoError(t, err)
	require.Equal(t, "default", got.(map[string]any)["namespace"])
}

func TestVerifyTable(t *testing.T) {
	fa := &fakeAdmin{tables: []*pb.TableName{tableName("default", "books"), tableName("app", "events")}}
	st := newFakeStore(newFakeClient(), fa, "", typeMap{}, ctAuto)
	require.NoError(t, st.verifyTable(context.Background(), "books"))
	require.NoError(t, st.verifyTable(context.Background(), "app:events"))
	require.ErrorContains(t, st.verifyTable(context.Background(), "missing"), "not found")
	// Namespace scoping itself is gohbase's ListNamespace (server-side); the fake
	// returns every table, so this test covers only the qualifier match and error.
}

func TestQueryPutBoolColumn(t *testing.T) {
	fc := newFakeClient()
	types := typeMap{cellKey("cf", "active"): ctBool}
	st := newFakeStore(fc, &fakeAdmin{}, "books", types, ctAuto)
	_, err := st.Query(context.Background(), []string{"put", "books", "1", "cf:active", "true"})
	require.NoError(t, err)
	got, err := st.Get(context.Background(), []string{"1"})
	require.NoError(t, err)
	require.Equal(t, true, got["1"].(map[string]any)["cf"].(map[string]any)["active"])
}

func TestFormatRaw(t *testing.T) {
	st := newFakeStore(newFakeClient(), &fakeAdmin{}, "books", typeMap{}, ctAuto)
	require.Contains(t, st.FormatRaw(map[string]any{"a": 1}, false), `"a"`)
	// A value JSON cannot encode falls back to a plain string form rather than panicking.
	require.NotEmpty(t, st.FormatRaw(make(chan int), false))
}

func TestTraceOpWritesWhenEnabled(t *testing.T) {
	fc := newFakeClient()
	fc.seed("1", cell("cf", "n", "x"))
	var buf bytes.Buffer
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	st.trace = &buf
	_, err := st.Get(context.Background(), []string{"1"})
	require.NoError(t, err)
	require.Contains(t, buf.String(), "hbase> get books")
}

func TestCloseClosesClient(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	require.NoError(t, st.Close())
	require.Equal(t, 1, fc.closeCalls)
}

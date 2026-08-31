package couchdb

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-kivik/kivik/v4/driver"
	"github.com/go-kivik/kivik/v4/mockdb"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// A CouchDB server cannot be told to fail a single row, lose a revision, or
// answer a bulk write with a per-document conflict, so the driver's failure and
// skip paths are driven through kivik's own mock driver instead: the Store holds
// a *kivik.Client, and mockdb hands back one whose every call is scripted. These
// tests need no container and run under -short.

// newMockStore returns a Store backed by mockdb, the mock client, and the mock
// database handle that every s.client.DB(...) call resolves to. dbCalls is how
// many times the method under test resolves that handle (upsert, Clear and
// Delete each resolve it twice, once for the revision read); each resolution
// consumes one expectation. Expectation order is not enforced, so a test reads
// as the call sequence rather than as bookkeeping.
func newMockStore(t *testing.T, pageSize, dbCalls int) (*Store, *mockdb.Client, *mockdb.DB) {
	t.Helper()
	client, mock, err := mockdb.New()
	require.NoError(t, err)
	mock.MatchExpectationsInOrder(false)
	mdb := mock.NewDB()
	for range dbCalls {
		mock.ExpectDB().WillReturn(mdb)
	}
	t.Cleanup(func() { _ = client.Close() })
	return &Store{client: client, db: "iq", pageSize: pageSize}, mock, mdb
}

// docRow builds an _all_docs/_find row carrying a document body.
func docRow(id, doc string) *driver.Row {
	return &driver.Row{ID: id, Doc: strings.NewReader(doc)}
}

// revRow builds a rev-less _all_docs row, whose value carries the revision.
func revRow(id, rev string) *driver.Row {
	return &driver.Row{ID: id, Value: strings.NewReader(`{"rev":"` + rev + `"}`)}
}

func TestGetSkipsARowWithNoBody(t *testing.T) {
	// A missing or deleted key comes back as an error row with no document. It is
	// left out of the map, and the rows after it are still read: the skip must not
	// end the walk.
	st, _, mdb := newMockStore(t, 10, 1)
	mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().
		AddRow(docRow("1", `{"_id":"1","title":"one"}`)).
		AddRow(&driver.Row{ID: "gone", Error: errors.New("not_found")}).
		AddRow(docRow("3", `{"_id":"3","title":"three"}`)))

	got, err := st.Get(context.Background(), []string{"1", "gone", "3"})
	require.NoError(t, err)
	require.Equal(t, []string{"1", "3"}, sortedKeys(got))
	require.Equal(t, "three", got["3"].(map[string]any)["title"])
}

func TestGetSurfacesADecodeFailure(t *testing.T) {
	// A body that is not a JSON object fails the decode, and the failure is
	// returned rather than swallowed into a partial map.
	st, _, mdb := newMockStore(t, 10, 1)
	mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().AddRow(docRow("1", `[1,2]`)))

	got, err := st.Get(context.Background(), []string{"1"})
	require.Nil(t, got)
	require.ErrorContains(t, err, "decode couchdb document")
}

func TestGetSurfacesAnIterationFailure(t *testing.T) {
	// A failure part way through _all_docs is reported against the driver, not
	// returned as a short result.
	st, _, mdb := newMockStore(t, 10, 1)
	boom := errors.New("connection reset")
	mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().
		AddRow(docRow("1", `{"_id":"1"}`)).
		AddRowError(boom))

	got, err := st.Get(context.Background(), []string{"1"})
	require.Nil(t, got)
	require.ErrorContains(t, err, "couchdb all_docs")
	require.ErrorIs(t, err, boom)
}

func TestGetWithNoKeysMakesNoRequest(t *testing.T) {
	// No keys short-circuits before the client is touched at all: the mock has no
	// expectation, so any request would fail the call.
	st, mock, _ := newMockStore(t, 10, 0)

	got, err := st.Get(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPutWithAnEmptyBatchMakesNoRequest(t *testing.T) {
	// An empty batch is a no-op with no revision read and no bulk write.
	st, mock, _ := newMockStore(t, 10, 0)

	stat, err := st.Put(context.Background(), nil, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{}, stat)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueryOmitsAnEmptyBookmark(t *testing.T) {
	// The bookmark is surfaced only when the server actually sent one; a reply
	// without one carries no bookmark key at all, so a caller cannot page on "".
	st, _, mdb := newMockStore(t, 10, 1)
	mdb.ExpectFind().WillReturn(mockdb.NewRows().AddRow(docRow("1", `{"_id":"1","a":1}`)))

	res, err := st.Query(context.Background(), []string{`{"a":1}`})
	require.NoError(t, err)
	out, ok := res.(map[string]any)
	require.True(t, ok)
	require.Len(t, out["docs"], 1)
	require.NotContains(t, out, "bookmark")
}

func TestQueryRejectsUnparsableJSON(t *testing.T) {
	// A malformed Mango document is rejected at parse time, before any _find
	// request is issued.
	st, mock, _ := newMockStore(t, 10, 0)

	res, err := st.Query(context.Background(), []string{`{not json`})
	require.Nil(t, res)
	require.ErrorContains(t, err, "parse couchdb mango query")
	require.NoError(t, mock.ExpectationsWereMet())

	// The decoder's own error stays reachable through the wrap.
	var syntax *json.SyntaxError
	require.ErrorAs(t, err, &syntax)
}

func TestCloseSurfacesTheClientError(t *testing.T) {
	// Close reports what the client reports; it is not a silent success.
	client, mock, err := mockdb.New()
	require.NoError(t, err)
	boom := errors.New("close failed")
	mock.ExpectClose().WillReturnError(boom)

	st := &Store{client: client, db: "iq", pageSize: 10}
	require.ErrorIs(t, st.Close(), boom)
}

func TestFindPagedSurfacesScanFailure(t *testing.T) {
	// A _find row that carries an error instead of a document fails the scan with
	// the scan message, distinct from a decode failure.
	st, _, mdb := newMockStore(t, 10, 1)
	boom := errors.New("row failed")
	mdb.ExpectFind().WillReturn(mockdb.NewRows().
		AddRow(&driver.Row{ID: "1", Error: boom}))

	err := st.findPaged(context.Background(), map[string]any{"a": 1}, func(map[string]any) error { return nil })
	require.ErrorContains(t, err, "couchdb scan document")
	require.ErrorIs(t, err, boom)
}

func TestFindPagedSurfacesDecodeFailure(t *testing.T) {
	// A body that is not an object fails the page rather than reaching the caller
	// as a document with an empty id.
	st, _, mdb := newMockStore(t, 10, 1)
	mdb.ExpectFind().WillReturn(mockdb.NewRows().AddRow(docRow("1", `[1,2]`)))

	pages := 0
	err := st.findPaged(context.Background(), map[string]any{"a": 1}, func(map[string]any) error {
		pages++
		return nil
	})
	require.ErrorContains(t, err, "decode couchdb document")
	require.Zero(t, pages, "no page reaches the caller once a document fails to decode")
}

func TestFindPagedSurfacesIterationFailure(t *testing.T) {
	// A failure part way through _find is returned, not reported as a finished walk.
	st, _, mdb := newMockStore(t, 10, 1)
	boom := errors.New("connection reset")
	mdb.ExpectFind().WillReturn(mockdb.NewRows().
		AddRow(docRow("1", `{"_id":"1"}`)).
		AddRowError(boom))

	err := st.findPaged(context.Background(), map[string]any{"a": 1}, func(map[string]any) error { return nil })
	require.ErrorContains(t, err, "couchdb find")
	require.ErrorIs(t, err, boom)
}

func TestFindPagedStopsWhenTheServerSendsNoBookmark(t *testing.T) {
	// A full page with no bookmark to follow ends the walk: only one _find is
	// issued, so the single expectation is enough and a second request would fail.
	st, mock, mdb := newMockStore(t, 1, 1)
	mdb.ExpectFind().WillReturn(mockdb.NewRows().AddRow(docRow("1", `{"_id":"1","a":1}`)))

	var pages []int
	require.NoError(t, st.findPaged(context.Background(), map[string]any{"a": 1}, func(batch map[string]any) error {
		pages = append(pages, len(batch))
		return nil
	}))
	require.Equal(t, []int{1}, pages)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDropSurfacesTheDestroyFailure(t *testing.T) {
	client, mock, err := mockdb.New()
	require.NoError(t, err)
	boom := errors.New("forbidden")
	mock.ExpectDestroyDB().WillReturnError(boom)
	t.Cleanup(func() { _ = client.Close() })

	st := &Store{client: client, db: "iq", pageSize: 10}
	err = st.Drop(context.Background())
	require.ErrorContains(t, err, "couchdb destroy db")
	require.ErrorIs(t, err, boom)
}

func TestClearSurfacesTheBulkDeleteFailure(t *testing.T) {
	st, _, mdb := newMockStore(t, 10, 2)
	boom := errors.New("service unavailable")
	mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().AddRow(revRow("1", "1-a")))
	mdb.ExpectBulkDocs().WillReturnError(boom)

	err := st.Clear(context.Background())
	require.ErrorContains(t, err, "couchdb bulk delete")
	require.ErrorIs(t, err, boom)
}

func TestUpsertSurfacesAPerDocumentFailure(t *testing.T) {
	// A bulk write that succeeds as a request but reports a per-document failure
	// is an error, never a silently miscounted write.
	st, _, mdb := newMockStore(t, 10, 2)
	boom := errors.New("conflict")
	mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().AddRow(revRow("1", "1-a")))
	mdb.ExpectBulkDocs().WillReturn([]driver.BulkResult{{ID: "1", Error: boom}})

	stat, err := st.Put(context.Background(),
		[]query.Record{{Key: "1", Value: map[string]any{"title": "one"}}}, query.Upsert)
	require.Equal(t, query.WriteStat{}, stat)
	require.ErrorContains(t, err, "couchdb bulk write")
	require.ErrorIs(t, err, boom)
}

func TestDeleteSurfacesAPerDocumentFailure(t *testing.T) {
	// A stale-rev conflict under a concurrent write is returned, naming the key,
	// rather than counted as a deletion.
	st, _, mdb := newMockStore(t, 10, 2)
	boom := errors.New("conflict")
	mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().AddRow(revRow("1", "1-a")))
	mdb.ExpectBulkDocs().WillReturn([]driver.BulkResult{{ID: "1", Error: boom}})

	stat, err := st.Delete(context.Background(), []string{"1"})
	require.Equal(t, query.DeleteStat{}, stat)
	require.ErrorContains(t, err, `couchdb delete "1"`)
	require.ErrorIs(t, err, boom)
}

func TestScanDeletesSkipsRowsWithNothingToTombstone(t *testing.T) {
	// A tombstone needs both an id and a revision. A row with neither is skipped,
	// so no {"_id":""} deletion is ever written.
	st, _, mdb := newMockStore(t, 10, 1)
	mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().
		AddRow(revRow("", "1-a")).
		AddRow(&driver.Row{ID: "norev"}).
		AddRow(revRow("_design/idx", "1-b")).
		AddRow(revRow("keep", "1-c")))

	var got []any
	require.NoError(t, st.scanDeletes(context.Background(), func(batch []any) error {
		got = append(got, batch...)
		return nil
	}))
	require.Equal(t, []any{map[string]any{"_id": "keep", "_rev": "1-c", "_deleted": true}}, got)
}

func TestInspectServerReportsTheWholeVersion(t *testing.T) {
	client, mock, err := mockdb.New()
	require.NoError(t, err)
	mock.MatchExpectationsInOrder(false)
	mock.ExpectVersion().WillReturn(&driver.Version{
		Version:  "3.3.3",
		Vendor:   "The Apache Software Foundation",
		Features: []string{"access-ready", "partitioned", "pluggable-storage-engines"},
	})
	t.Cleanup(func() { _ = client.Close() })

	st := &Store{client: client, db: "iq", pageSize: 10}
	res, err := st.InspectServer(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"version": "3.3.3",
		"vendor":  "The Apache Software Foundation",
		"features": []any{
			"access-ready", "partitioned", "pluggable-storage-engines",
		},
	}, res)
}

func TestInspectDatabasesReportsEveryName(t *testing.T) {
	client, mock, err := mockdb.New()
	require.NoError(t, err)
	mock.MatchExpectationsInOrder(false)
	mock.ExpectAllDBs().WillReturn([]string{"_users", "books", "shop"})
	t.Cleanup(func() { _ = client.Close() })

	st := &Store{client: client, db: "iq", pageSize: 10}
	res, err := st.InspectDatabases(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[string]any{"databases": []any{"_users", "books", "shop"}}, res)
}

func TestInspectDBInfoReportsEveryStatistic(t *testing.T) {
	st, _, mdb := newMockStore(t, 10, 1)
	mdb.ExpectStats().WillReturn(&driver.DBStats{
		Name:         "books",
		DocCount:     42,
		DeletedCount: 7,
		DiskSize:     8192,
		ActiveSize:   4096,
		UpdateSeq:    "13-g1AAAA",
	})

	res, err := st.InspectDBInfo(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"name":         "books",
		"docCount":     int64(42),
		"deletedCount": int64(7),
		"diskSize":     int64(8192),
		"dataSize":     int64(4096),
		"updateSeq":    "13-g1AAAA",
	}, res)
}

func TestInspectIndexesReportsEveryIndex(t *testing.T) {
	st, _, mdb := newMockStore(t, 10, 1)
	mdb.ExpectGetIndexes().WillReturn([]driver.Index{
		{Name: "_all_docs", Type: "special", Definition: map[string]any{"fields": []any{"_id"}}},
		{DesignDoc: "_design/byyear", Name: "by-year", Type: "json", Definition: map[string]any{"fields": []any{"year"}}},
	})

	res, err := st.InspectIndexes(context.Background())
	require.NoError(t, err)
	require.Equal(t, map[string]any{"indexes": []any{
		map[string]any{
			"designDoc":  "",
			"name":       "_all_docs",
			"type":       "special",
			"definition": map[string]any{"fields": []any{"_id"}},
		},
		map[string]any{
			"designDoc":  "_design/byyear",
			"name":       "by-year",
			"type":       "json",
			"definition": map[string]any{"fields": []any{"year"}},
		},
	}}, res)
}

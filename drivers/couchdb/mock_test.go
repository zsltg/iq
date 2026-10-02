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

func TestScanBatchesSurfacesARowFailure(t *testing.T) {
	// Each failure on the _all_docs walk is returned with its own message, and no
	// page reaches the caller. A row with an error fails at its id, a row with no
	// body fails at the scan, and a body that is not an object fails at the decode.
	boom := errors.New("row failed")
	tests := []struct {
		name    string
		row     *driver.Row
		wantErr string
		wrapped bool
	}{
		{"row error", &driver.Row{ID: "1", Error: boom}, "couchdb row id", true},
		{"row with no body", &driver.Row{ID: "1"}, "couchdb scan document", true},
		{"body that is not an object", docRow("1", `[1,2]`), "decode couchdb document", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, mdb := newMockStore(t, 10, 1)
			mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().
				AddRow(tt.row).
				AddRow(docRow("2", `{"_id":"2"}`)))

			pages := 0
			err := st.ScanBatches(context.Background(), func(map[string]any) error {
				pages++
				return nil
			})

			require.ErrorContains(t, err, tt.wantErr)
			if tt.wrapped {
				require.Error(t, errors.Unwrap(err))
			}
			require.Zero(t, pages)
		})
	}
}

func TestScanBatchesSurfacesAnIterationFailure(t *testing.T) {
	// A failure part way through _all_docs is returned, not reported as a finished
	// walk, and the page read before it is not handed on.
	st, _, mdb := newMockStore(t, 10, 1)
	boom := errors.New("connection reset")
	mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().
		AddRow(docRow("1", `{"_id":"1"}`)).
		AddRowError(boom))

	pages := 0
	err := st.ScanBatches(context.Background(), func(map[string]any) error {
		pages++
		return nil
	})

	require.ErrorContains(t, err, "couchdb all_docs")
	require.ErrorIs(t, err, boom)
	require.Zero(t, pages)
}

func TestScanBatchesReadsPastADesignDocument(t *testing.T) {
	// A design document is skipped, and the walk goes on to the documents after it.
	st, _, mdb := newMockStore(t, 10, 1)
	mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().
		AddRow(docRow("_design/idx", `{"_id":"_design/idx"}`)).
		AddRow(docRow("1", `{"_id":"1","title":"one"}`)))

	got := collect(t, func(fn func(map[string]any) error) error {
		return st.ScanBatches(context.Background(), fn)
	})

	require.Equal(t, []string{"1"}, sortedKeys(got))
}

func TestEstimateCountSurfacesTheStatsFailure(t *testing.T) {
	st, _, mdb := newMockStore(t, 10, 1)
	boom := errors.New("not found")
	mdb.ExpectStats().WillReturnError(boom)

	n, err := st.EstimateCount(context.Background())

	require.ErrorContains(t, err, "couchdb db stats")
	require.ErrorIs(t, err, boom)
	require.Zero(t, n)
}

func TestQuerySurfacesARowFailure(t *testing.T) {
	// Each failure on the _find walk of a raw query is returned with its own
	// message, and no partial reply is returned.
	boom := errors.New("row failed")
	tests := []struct {
		name    string
		rows    *mockdb.Rows
		wantErr string
		wrapped bool
	}{
		{"row error", mockdb.NewRows().AddRow(&driver.Row{ID: "1", Error: boom}), "couchdb scan document", true},
		{"body that is not an object", mockdb.NewRows().AddRow(docRow("1", `[1,2]`)), "decode couchdb document", false},
		{"iteration failure", mockdb.NewRows().AddRow(docRow("1", `{"_id":"1"}`)).AddRowError(boom), "couchdb find", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, mdb := newMockStore(t, 10, 1)
			mdb.ExpectFind().WillReturn(tt.rows)

			res, err := st.Query(context.Background(), []string{`{"a":1}`})

			require.ErrorContains(t, err, tt.wantErr)
			if tt.wrapped {
				require.Error(t, errors.Unwrap(err))
			}
			require.Nil(t, res)
		})
	}
}

func TestInspectSurfacesTheServerFailure(t *testing.T) {
	// Every inspect call returns the failure of its request with its own message.
	boom := errors.New("unauthorized")
	tests := []struct {
		name    string
		expect  func(mock *mockdb.Client, mdb *mockdb.DB)
		dbCalls int
		run     func(st *Store) (any, error)
		wantErr string
	}{
		{
			name:    "server",
			expect:  func(mock *mockdb.Client, _ *mockdb.DB) { mock.ExpectVersion().WillReturnError(boom) },
			run:     func(st *Store) (any, error) { return st.InspectServer(context.Background()) },
			wantErr: "couchdb server version",
		},
		{
			name:    "databases",
			expect:  func(mock *mockdb.Client, _ *mockdb.DB) { mock.ExpectAllDBs().WillReturnError(boom) },
			run:     func(st *Store) (any, error) { return st.InspectDatabases(context.Background()) },
			wantErr: "couchdb list databases",
		},
		{
			name:    "database info",
			expect:  func(_ *mockdb.Client, mdb *mockdb.DB) { mdb.ExpectStats().WillReturnError(boom) },
			dbCalls: 1,
			run:     func(st *Store) (any, error) { return st.InspectDBInfo(context.Background()) },
			wantErr: "couchdb db info",
		},
		{
			name:    "indexes",
			expect:  func(_ *mockdb.Client, mdb *mockdb.DB) { mdb.ExpectGetIndexes().WillReturnError(boom) },
			dbCalls: 1,
			run:     func(st *Store) (any, error) { return st.InspectIndexes(context.Background()) },
			wantErr: "couchdb list indexes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, mock, mdb := newMockStore(t, 10, tt.dbCalls)
			tt.expect(mock, mdb)

			res, err := tt.run(st)

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, boom)
			require.Nil(t, res)
		})
	}
}

func TestUpsertSurfacesTheRevisionReadFailure(t *testing.T) {
	// When the read of the current revisions fails, nothing is written: the mock
	// has no bulk write to answer.
	st, _, mdb := newMockStore(t, 10, 2)
	boom := errors.New("connection reset")
	mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().AddRowError(boom))

	stat, err := st.Put(context.Background(),
		[]query.Record{{Key: "1", Value: map[string]any{"title": "one"}}}, query.Upsert)

	require.ErrorContains(t, err, "couchdb read revisions")
	require.ErrorIs(t, err, boom)
	require.Equal(t, query.WriteStat{}, stat)
}

func TestPutSurfacesTheBulkWriteFailure(t *testing.T) {
	// A bulk write that fails as a request is returned under both write modes.
	boom := errors.New("service unavailable")
	tests := []struct {
		name    string
		mode    query.WriteMode
		dbCalls int
	}{
		{"upsert", query.Upsert, 2},
		{"insert only", query.InsertOnly, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, mdb := newMockStore(t, 10, tt.dbCalls)
			mdb.ExpectAllDocs().WillReturn(mockdb.NewRows())
			mdb.ExpectBulkDocs().WillReturnError(boom)

			stat, err := st.Put(context.Background(),
				[]query.Record{{Key: "1", Value: map[string]any{"title": "one"}}}, tt.mode)

			require.ErrorContains(t, err, "couchdb bulk write")
			require.ErrorIs(t, err, boom)
			require.Equal(t, query.WriteStat{}, stat)
		})
	}
}

func TestInsertOnlyRejectsANonObjectValue(t *testing.T) {
	// A scalar value is rejected before any request is sent.
	st, mock, _ := newMockStore(t, 10, 0)

	stat, err := st.Put(context.Background(),
		[]query.Record{{Key: "1", Value: "scalar"}}, query.InsertOnly)

	require.ErrorContains(t, err, `couchdb: value for key "1" is not a JSON object`)
	require.Equal(t, query.WriteStat{}, stat)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeleteCountsARowWithNoRevisionAsMissing(t *testing.T) {
	// A row with an id but no revision has no live document to tombstone, so the
	// key is Missing. The rows after it are still read.
	st, _, mdb := newMockStore(t, 10, 2)
	mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().
		AddRow(&driver.Row{ID: "norev"}).
		AddRow(revRow("live", "1-a")))
	mdb.ExpectBulkDocs().WillReturn([]driver.BulkResult{{ID: "live", Rev: "2-b"}})

	stat, err := st.Delete(context.Background(), []string{"norev", "live"})

	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Deleted: 1, Missing: 1}, stat)
}

func TestDeleteDeletesTheKeysAfterAMissingOne(t *testing.T) {
	// A missing key is counted, and the keys after it are still deleted.
	st, _, mdb := newMockStore(t, 10, 2)
	mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().AddRow(revRow("live", "1-a")))
	mdb.ExpectBulkDocs().WillReturn([]driver.BulkResult{{ID: "live", Rev: "2-b"}})

	stat, err := st.Delete(context.Background(), []string{"gone", "live"})

	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Deleted: 1, Missing: 1}, stat)
}

func TestDeleteSurfacesARequestFailure(t *testing.T) {
	// A failed revision read and a failed bulk delete are both returned with a zero
	// count.
	boom := errors.New("service unavailable")
	tests := []struct {
		name    string
		expect  func(mdb *mockdb.DB)
		wantErr string
	}{
		{
			name:    "revision read",
			expect:  func(mdb *mockdb.DB) { mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().AddRowError(boom)) },
			wantErr: "couchdb read revisions",
		},
		{
			name: "bulk delete",
			expect: func(mdb *mockdb.DB) {
				mdb.ExpectAllDocs().WillReturn(mockdb.NewRows().AddRow(revRow("1", "1-a")))
				mdb.ExpectBulkDocs().WillReturnError(boom)
			},
			wantErr: "couchdb bulk delete",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, mdb := newMockStore(t, 10, 2)
			tt.expect(mdb)

			stat, err := st.Delete(context.Background(), []string{"1"})

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, boom)
			require.Equal(t, query.DeleteStat{}, stat)
		})
	}
}

func TestScanDeletesSurfacesAReadFailure(t *testing.T) {
	// A row error and an iteration failure on the _all_docs walk are both
	// returned, and no batch reaches the caller.
	boom := errors.New("row failed")
	tests := []struct {
		name    string
		rows    *mockdb.Rows
		wantErr string
	}{
		{"row error", mockdb.NewRows().AddRow(&driver.Row{ID: "1", Error: boom}).AddRow(revRow("2", "1-a")), "couchdb row id"},
		{"iteration failure", mockdb.NewRows().AddRow(revRow("1", "1-a")).AddRowError(boom), "couchdb all_docs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, mdb := newMockStore(t, 10, 1)
			mdb.ExpectAllDocs().WillReturn(tt.rows)

			batches := 0
			err := st.scanDeletes(context.Background(), func([]any) error {
				batches++
				return nil
			})

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, boom)
			require.Zero(t, batches)
		})
	}
}

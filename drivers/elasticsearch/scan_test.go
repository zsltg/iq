package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/rawpred"
)

// rejectNotOne is a matcher that proves every document with n other than 1 cannot
// match.
func rejectNotOne() *rawpred.Matcher {
	return rawpred.NewMatcher(predicate.Eq{Path: []string{"n"}, Value: 1.0})
}

// searchBodyOf decodes the body of search i that the stub recorded.
func searchBodyOf(t *testing.T, stub *pitStub, i int) map[string]any {
	t.Helper()
	require.Greater(t, len(stub.searches), i, "the stub received search %d", i)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(stub.searches[i]), &body))
	return body
}

// sortClient is an esClient whose scan sort is fixed, so a test can show that the scan
// takes its sort from the client.
type sortClient struct {
	esClient
	sort []any
}

func (c sortClient) scanSort() []any { return c.sort }

func TestScanClosesThePointInTime(t *testing.T) {
	// A scan holds a point-in-time on the server. It must release the one it holds on
	// every exit path, or the server keeps the resource until the keep_alive runs out.
	stub := &pitStub{pages: []string{`{"pit_id":"PIT-1","hits":{"hits":[]}}`}}
	st := newStubStore(t, "books", stub.handler)

	require.NoError(t, st.ScanBatches(t.Context(), func(map[string]any) error { return nil }))
	require.JSONEq(t, `{"id":"PIT-1"}`, stub.closed, "the scan releases the point-in-time it opened")
}

func TestScanAdoptsTheRotatedPointInTime(t *testing.T) {
	// A search can hand back a new point-in-time id. The next page must page against
	// the new one, and the close must release it. A scan that keeps the first id pages
	// against an id the server has already replaced.
	stub := &pitStub{pages: []string{
		`{"pit_id":"PIT-2","hits":{"hits":[{"_id":"1","_source":{"n":1},"sort":[1]}]}}`,
		`{"pit_id":"PIT-2","hits":{"hits":[]}}`,
	}}
	st := newStubStore(t, "books", stub.handler)
	st.pageSize = 1

	require.NoError(t, st.ScanBatches(t.Context(), func(map[string]any) error { return nil }))
	require.Len(t, stub.searches, 2, "a full page is followed by another search")

	var second map[string]any
	require.NoError(t, json.Unmarshal([]byte(stub.searches[1]), &second))
	pit, ok := second["pit"].(map[string]any)
	require.True(t, ok, "each search carries a pit clause")
	require.Equal(t, "PIT-2", pit["id"], "the next page uses the rotated id")
	require.Equal(t, []any{float64(1)}, second["search_after"],
		"the next page starts after the last hit's sort value")
	require.JSONEq(t, `{"id":"PIT-2"}`, stub.closed, "the close releases the rotated id")
}

func TestScanReportsASearchRefusal(t *testing.T) {
	// A refused search must stop the scan and carry the server's reason. A scan that
	// ignores it reads the zero reply, sees no hits, and reports a clean finish over an
	// index it never read.
	st := newStubStore(t, "books", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_search" {
			esJSON(w, http.StatusServiceUnavailable,
				`{"error":{"type":"search_phase_execution_exception","reason":"all shards failed"}}`)
			return
		}
		esJSON(w, http.StatusOK, `{"id":"PIT-1"}`)
	})

	pages := 0
	err := st.ScanBatches(t.Context(), func(map[string]any) error { pages++; return nil })
	require.ErrorContains(t, err, "search_phase_execution_exception")
	require.ErrorContains(t, err, "all shards failed")
	require.Zero(t, pages, "no page reaches the caller when the search fails")
}

func TestScanReportsAnUndecodableSource(t *testing.T) {
	// A hit whose _source is not an object cannot become a document. The scan must stop
	// and say so, not hand the caller an empty value under that key.
	stub := &pitStub{pages: []string{`{"pit_id":"PIT-1","hits":{"hits":[{"_id":"1","_source":42,"sort":[1]}]}}`}}
	st := newStubStore(t, "books", stub.handler)

	pages := 0
	err := st.ScanBatches(t.Context(), func(map[string]any) error { pages++; return nil })
	require.ErrorContains(t, err, "decode elasticsearch _source")
	require.Zero(t, pages, "a page with an undecodable hit never reaches the caller")
}

func TestScanStopsAtTheCallersRefusal(t *testing.T) {
	// The caller can stop a scan by returning an error. The scan must give that error
	// back and read no further page.
	stub := &pitStub{pages: []string{
		`{"pit_id":"PIT-1","hits":{"hits":[{"_id":"1","_source":{"n":1},"sort":[1]}]}}`,
		`{"pit_id":"PIT-1","hits":{"hits":[{"_id":"2","_source":{"n":2},"sort":[2]}]}}`,
		// A last empty page, so a scan that ignores the refusal still ends and fails on
		// the counts below instead of paging for ever.
		`{"pit_id":"PIT-1","hits":{"hits":[]}}`,
	}}
	st := newStubStore(t, "books", stub.handler)
	st.pageSize = 1

	want := errors.New("caller stopped")
	pages := 0
	err := st.ScanBatches(t.Context(), func(map[string]any) error { pages++; return want })
	require.ErrorIs(t, err, want)
	require.Equal(t, 1, pages, "the scan stops at the first refusal")
	require.Len(t, stub.searches, 1, "no further page is requested")
}

func TestGetReportsARefusedRead(t *testing.T) {
	// A refused mget must reach the caller. A guard that ignores it returns the empty
	// map the call started with, which reads as "no document matched those keys".
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusForbidden, `{"error":{"type":"security_exception","reason":"denied"}}`)
	})
	got, err := st.Get(t.Context(), []string{"1", "2"})
	require.Nil(t, got, "a refused read gives no partial map")
	require.ErrorContains(t, err, "elasticsearch mget: security_exception")
	require.ErrorContains(t, err, "denied")
}

func TestQueryRejectsAMalformedBody(t *testing.T) {
	// A search body that is not JSON must stop before a request goes out, with a
	// message that names the fault.
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusOK, `{"hits":{"hits":[]}}`)
	})
	got, err := st.Query(t.Context(), []string{`{"term":`})
	require.Nil(t, got)
	require.ErrorContains(t, err, "parse search body")
	var syntax *json.SyntaxError
	require.ErrorAs(t, err, &syntax, "the cause stays reachable through the wrap")
}

func TestQueryReportsAMalformedReply(t *testing.T) {
	// A success status with a body that is not JSON must fail, not hand the caller a
	// nil reply and no error.
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusOK, `not-json`)
	})
	got, err := st.Query(t.Context(), []string{`{"match_all":{}}`})
	require.Nil(t, got)
	require.ErrorContains(t, err, "elasticsearch search: decode response")
	var syntax *json.SyntaxError
	require.ErrorAs(t, err, &syntax, "the cause stays reachable through the wrap")
}

func TestCloseReportsTheClientFailure(t *testing.T) {
	// Close is the store's only release path. It must pass the client's verdict on, so
	// a client that cannot release its resources is not reported as a clean close.
	want := errors.New("transport shutdown failed")
	st := &Store{client: closeErrClient{err: want}}
	require.ErrorIs(t, st.Close(), want)
}

func TestPrefilteredScanKeepsTheCursorOfASkippedLastHit(t *testing.T) {
	// The cursor moves past every hit, kept or skipped. If the last hit of a page is
	// skipped, the next search must still start after that hit, or the scan reads it
	// again for ever.
	stub := &pitStub{pages: []string{
		`{"pit_id":"PIT-1","hits":{"hits":[{"_id":"a","_source":{"n":1},"sort":[10]},{"_id":"b","_source":{"n":2},"sort":[20]}]}}`,
		`{"pit_id":"PIT-1","hits":{"hits":[]}}`,
	}}
	st := newStubStore(t, "books", stub.handler)
	st.pageSize = 2

	var got []map[string]any
	err := st.pagedSearch(t.Context(), nil, rejectNotOne(), func(b map[string]any) error {
		got = append(got, b)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, got, 1, "one page reaches the caller")
	require.Len(t, got[0], 1, "only the kept hit is delivered")
	require.Contains(t, got[0], "a")
	require.Equal(t, []any{float64(20)}, searchBodyOf(t, stub, 1)["search_after"],
		"the next search starts after the skipped hit")
	require.Equal(t, 2, st.prefilterChecked, "every hit is checked")
	require.Equal(t, 1, st.prefilterSkipped, "the rejected hit is counted")
}

func TestPrefilteredScanHidesAnEmptiedPage(t *testing.T) {
	// A page that the prefilter empties does not reach the caller, and the scan goes on
	// to the next page.
	stub := &pitStub{pages: []string{
		`{"pit_id":"PIT-1","hits":{"hits":[{"_id":"a","_source":{"n":2},"sort":[1]},{"_id":"b","_source":{"n":3},"sort":[2]}]}}`,
		`{"pit_id":"PIT-1","hits":{"hits":[{"_id":"c","_source":{"n":1},"sort":[3]}]}}`,
	}}
	st := newStubStore(t, "books", stub.handler)
	st.pageSize = 2

	var pages []int
	err := st.pagedSearch(t.Context(), nil, rejectNotOne(), func(b map[string]any) error {
		pages = append(pages, len(b))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []int{1}, pages, "the emptied page is not delivered, the next one is")
	require.Len(t, stub.searches, 2, "the scan reads on past the emptied page")
	require.Equal(t, []any{float64(2)}, searchBodyOf(t, stub, 1)["search_after"])
	require.Equal(t, 3, st.prefilterChecked)
	require.Equal(t, 2, st.prefilterSkipped)
}

func TestUnfilteredScanCountsNoPrefilter(t *testing.T) {
	// Without a matcher, no hit is checked or skipped.
	stub := &pitStub{pages: []string{
		`{"pit_id":"PIT-1","hits":{"hits":[{"_id":"a","_source":{"n":2},"sort":[1]}]}}`,
	}}
	st := newStubStore(t, "books", stub.handler)

	require.NoError(t, st.ScanBatches(t.Context(), func(map[string]any) error { return nil }))
	require.Zero(t, st.prefilterChecked)
	require.Zero(t, st.prefilterSkipped)
}

func TestScanSearchBody(t *testing.T) {
	// The first search of a full scan sends size, track_total_hits, the flavor sort, and
	// the pit. It has no search_after and no query.
	stub := &pitStub{pages: []string{`{"pit_id":"PIT-1","hits":{"hits":[]}}`}}
	st := newStubStore(t, "books", stub.handler)
	st.pageSize = 7

	require.NoError(t, st.ScanBatches(t.Context(), func(map[string]any) error { return nil }))
	body := searchBodyOf(t, stub, 0)
	require.Equal(t, map[string]any{
		"size":             float64(7),
		"track_total_hits": false,
		"sort":             []any{map[string]any{"_shard_doc": "asc"}},
		"pit":              map[string]any{"id": "PIT-1", "keep_alive": "1m"},
	}, body)
}

func TestScanSearchBodyTakesTheSortFromTheClient(t *testing.T) {
	// OpenSearch has no _shard_doc, so its client sorts by _id. The scan must send the
	// sort the client gives.
	stub := &pitStub{pages: []string{`{"pit_id":"PIT-1","hits":{"hits":[]}}`}}
	st := newStubStore(t, "books", stub.handler)
	st.client = sortClient{esClient: st.client, sort: []any{map[string]any{"_id": "asc"}}}

	require.NoError(t, st.ScanBatches(t.Context(), func(map[string]any) error { return nil }))
	require.Equal(t, []any{map[string]any{"_id": "asc"}}, searchBodyOf(t, stub, 0)["sort"])
}

func TestFilteredScanSearchBodyCarriesThePushedQuery(t *testing.T) {
	// A pushed query goes into every search body. The first page has no search_after.
	stub := &pitStub{pages: []string{
		`{"pit_id":"PIT-1","hits":{"hits":[{"_id":"a","_source":{"n":1},"sort":[5]}]}}`,
		`{"pit_id":"PIT-1","hits":{"hits":[]}}`,
	}}
	st := newStubStore(t, "books", stub.handler)
	st.pageSize = 1
	query := map[string]any{"term": map[string]any{"n": float64(1)}}

	require.NoError(t, st.pagedSearch(t.Context(), query, nil, func(map[string]any) error { return nil }))
	first, second := searchBodyOf(t, stub, 0), searchBodyOf(t, stub, 1)
	require.Equal(t, query, first["query"])
	require.NotContains(t, first, "search_after")
	require.Equal(t, query, second["query"], "every page carries the query")
	require.Equal(t, []any{float64(5)}, second["search_after"])
}

func TestScanClosesThePointInTimeAfterCancellation(t *testing.T) {
	// The caller cancels the context after the point-in-time is open. The next search
	// fails, and the close request must still reach the server with the latest id. The
	// close request must not use the canceled context.
	stub := &pitStub{pages: []string{
		`{"pit_id":"PIT-2","hits":{"hits":[{"_id":"1","_source":{"n":1},"sort":[1]}]}}`,
		`{"pit_id":"PIT-3","hits":{"hits":[]}}`,
	}}
	st := newStubStore(t, "books", stub.handler)
	st.pageSize = 1

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := st.ScanBatches(ctx, func(map[string]any) error { cancel(); return nil })
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, stub.searches, 1, "no search runs on a canceled context")
	require.JSONEq(t, `{"id":"PIT-2"}`, stub.closed, "the close releases the latest id")
}

func TestScanClosesThePointInTimeAfterTheCallersRefusal(t *testing.T) {
	// A caller error ends the scan, and the point-in-time is still released.
	stub := &pitStub{pages: []string{
		`{"pit_id":"PIT-2","hits":{"hits":[{"_id":"1","_source":{"n":1},"sort":[1]}]}}`,
	}}
	st := newStubStore(t, "books", stub.handler)
	st.pageSize = 1

	want := errors.New("caller stopped")
	err := st.ScanBatches(t.Context(), func(map[string]any) error { return want })
	require.ErrorIs(t, err, want)
	require.JSONEq(t, `{"id":"PIT-2"}`, stub.closed)
}

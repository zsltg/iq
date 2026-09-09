package elasticsearch

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

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

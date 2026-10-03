package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

func TestOpenReportsAnUnusableURL(t *testing.T) {
	// A URL that cannot be parsed must stop Open with the parse fault. An Open that
	// goes on with an empty config reaches for a default server and reports a
	// different fault, or none.
	tests := []struct {
		name    string
		rawURL  string
		wantErr string
	}{
		{name: "port is not a number", rawURL: "elasticsearch://localhost:abc/", wantErr: "parse source url"},
		{name: "scheme is not supported", rawURL: "http://localhost:9200/", wantErr: "must use elasticsearch://"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := Open(t.Context(), tt.rawURL, "", nil, numfmt.DecimalAuto)
			require.Nil(t, st)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// boundedCtx returns a context that ends after 10 seconds or when the test ends, so a
// stalled stub fails the test instead of hanging it.
func boundedCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestParseURLKeepsTheParseCauseInTheChain(t *testing.T) {
	// The wrap must keep the cause, so a caller can tell a malformed URL from a bad
	// scheme.
	_, err := parseURL("elasticsearch://localhost:abc/", "")
	require.ErrorContains(t, err, "parse source url")
	var urlErr *url.Error
	require.ErrorAs(t, err, &urlErr, "the cause stays reachable through the wrap")
}

func TestScanAsksForAFullPage(t *testing.T) {
	// The default page size is 100. A scan that asks for another size changes how many
	// documents each request holds, and so the memory a scan needs. The size is only
	// visible in the body of the search.
	stub := &pitStub{pages: []string{`{"pit_id":"PIT-1","hits":{"hits":[]}}`}}
	st := newStubStore(t, "books", stub.handler)

	require.NoError(t, st.ScanBatches(boundedCtx(t), func(map[string]any) error { return nil }))
	require.Len(t, stub.searches, 1)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(stub.searches[0]), &body))
	require.InDelta(t, 100, body["size"], 0, "the search asks for the default page size")
}

func TestOpenSetsTheDefaultPageSize(t *testing.T) {
	// Open must give the store the default page size of 100.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusOK, `{}`)
	}))
	t.Cleanup(srv.Close)
	rawURL := "elasticsearch://" + srv.Listener.Addr().String() + "/"
	st, err := Open(boundedCtx(t), rawURL, "", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.Equal(t, 100, st.pageSize)
}

func TestSuccessStatusRangeEndsAt299(t *testing.T) {
	// Every status from 200 to 299 is a success. A status test that ends sooner turns
	// a valid reply into a failure.
	t.Run("request path", func(t *testing.T) {
		st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
			esJSON(w, 299, `{"count":7}`)
		})
		n, err := st.EstimateCount(boundedCtx(t))
		require.NoError(t, err)
		require.EqualValues(t, 7, n)
	})
	t.Run("raw query path", func(t *testing.T) {
		st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
			esJSON(w, 299, `{"hits":{"hits":[]}}`)
		})
		got, err := st.Query(boundedCtx(t), []string{`{"match_all":{}}`})
		require.NoError(t, err)
		require.NotNil(t, got)
	})
}

func TestRequestReportsABadMethod(t *testing.T) {
	// A request that cannot be built must stop before anything is sent, with the
	// cause behind the wrap.
	calls := 0
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		esJSON(w, http.StatusOK, `{}`)
	})
	err := st.request(boundedCtx(t), "probe", "BAD METHOD", "/", nil, nil)
	require.ErrorContains(t, err, "build request")
	require.Error(t, errors.Unwrap(errors.Unwrap(err)), "the cause stays reachable through both wraps")
	require.Zero(t, calls, "a request that cannot be built is never sent")
}

func TestScanReportsAnUnencodableQuery(t *testing.T) {
	// A pushed-down query that the JSON encoder cannot write must stop the scan with
	// a clear message, before any search goes out.
	stub := &pitStub{pages: []string{`{"pit_id":"PIT-1","hits":{"hits":[]}}`}}
	st := newStubStore(t, "books", stub.handler)

	pages := 0
	err := st.pagedSearch(boundedCtx(t), map[string]any{"bad": math.NaN()}, nil, func(map[string]any) error { pages++; return nil })
	require.ErrorContains(t, err, "encode search")
	var unsupported *json.UnsupportedValueError
	require.ErrorAs(t, err, &unsupported, "the cause stays reachable through the wrap")
	require.Zero(t, pages)
	require.Empty(t, stub.searches, "a query that cannot be encoded is never sent")
}

func TestScanReportsAPointInTimeRefusal(t *testing.T) {
	// A refused point-in-time must stop the scan with the server's reason. A scan that
	// goes on pages through an empty id.
	searches := 0
	st := newStubStore(t, "books", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_search" {
			searches++
			esJSON(w, http.StatusOK, `{"hits":{"hits":[]}}`)
			return
		}
		esJSON(w, http.StatusForbidden, `{"error":{"type":"security_exception","reason":"denied"}}`)
	})

	err := st.ScanBatches(boundedCtx(t), func(map[string]any) error { return nil })
	require.ErrorContains(t, err, "elasticsearch open point-in-time: security_exception")
	require.Zero(t, searches, "no search goes out without a point-in-time")
}

func TestGetReportsAnUndecodableSource(t *testing.T) {
	// A found document whose _source is not an object must stop the read. A read that
	// skips the fault returns an empty value under the key.
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusOK, `{"docs":[{"_id":"1","found":true,"_source":42}]}`)
	})
	got, err := st.Get(boundedCtx(t), []string{"1"})
	require.Nil(t, got)
	require.ErrorContains(t, err, "decode elasticsearch _source")
}

func TestQueryWrapsTheTransportFailure(t *testing.T) {
	// A server that is not there must give a labelled error with the cause behind it.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()
	c, err := newClient(connConfig{flavor: flavorES, addr: addr}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.close() })
	st := &Store{client: c, index: "books", pageSize: scanBatch}

	got, err := st.Query(boundedCtx(t), []string{`{"match_all":{}}`})
	require.Nil(t, got)
	require.ErrorContains(t, err, "elasticsearch search:")
	var netErr *net.OpError
	require.ErrorAs(t, err, &netErr, "the cause stays reachable through the wrap")
}

func TestPutRejectsAScalarValue(t *testing.T) {
	// A value that is not an object cannot be a document. Put must stop with the cause
	// and send nothing. A Put that goes on sends an action line with no document.
	calls := 0
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		esJSON(w, http.StatusOK, `{"items":[]}`)
	})
	stat, err := st.Put(boundedCtx(t), []query.Record{{Key: "1", Value: "scalar"}}, query.Upsert)
	require.Equal(t, query.WriteStat{}, stat)
	require.Error(t, err)
	require.ErrorContains(t, err, "elasticsearch")
	require.Zero(t, calls, "a batch with a scalar value is never sent")
}

func TestDeleteReportsAFailedItem(t *testing.T) {
	// A bulk delete that answers 200 but fails one item must fail the call with the
	// item's reason. A delete that drops the fault reports a clean run.
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusOK, `{"items":[{"delete":{"status":400,"error":{"type":"mapper_exception","reason":"bad id"}}}]}`)
	})
	stat, err := st.Delete(boundedCtx(t), []string{"1"})
	require.Equal(t, query.DeleteStat{}, stat)
	require.ErrorContains(t, err, "elasticsearch bulk delete: mapper_exception: bad id")
}

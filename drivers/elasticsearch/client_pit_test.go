package elasticsearch

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// flavors lists both adapters, with the wire facts that differ between them.
var flavors = []struct {
	name     string
	flavor   flavor
	label    string
	openPath string
	idKey    string
	otherKey string
}{
	{name: "elasticsearch", flavor: flavorES, label: "elasticsearch", openPath: "/books/_pit", idKey: "id", otherKey: "pit_id"},
	{name: "opensearch", flavor: flavorOS, label: "opensearch", openPath: "/books/_search/point_in_time", idKey: "pit_id", otherKey: "id"},
}

// replyServer answers every request with the given status and body.
func replyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, status, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestClient(t *testing.T, fl flavor, addr string) esClient {
	t.Helper()
	c, err := newClient(connConfig{flavor: fl, addr: addr}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.close() })
	return c
}

func TestOpenPITRequestShape(t *testing.T) {
	// The request carries the method, the path, the keep_alive and no body. A wrong
	// path still reaches a server and fails much later.
	for _, tt := range flavors {
		t.Run(tt.name, func(t *testing.T) {
			// The handler runs on the server goroutine, so it hands the request
			// fields over through a channel.
			type seen struct{ method, path, rawQuery, body string }
			got := make(chan seen, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				got <- seen{r.Method, r.URL.Path, r.URL.RawQuery, string(b)}
				esJSON(w, http.StatusOK, `{"`+tt.idKey+`":"P1"}`)
			}))
			t.Cleanup(srv.Close)
			c := newTestClient(t, tt.flavor, srv.URL)

			id, err := c.openPIT(t.Context(), "books")
			require.NoError(t, err)
			require.Equal(t, "P1", id)
			req := <-got
			require.Equal(t, http.MethodPost, req.method)
			require.Equal(t, tt.openPath, req.path)
			require.Equal(t, "keep_alive="+keepAlive, req.rawQuery)
			require.Empty(t, req.body)
		})
	}
}

func TestOpenPITReadsTheFlavorKey(t *testing.T) {
	// Each flavor reads its own id key. The key of the other flavor is not an id.
	for _, tt := range flavors {
		t.Run(tt.name, func(t *testing.T) {
			srv := replyServer(t, http.StatusOK, `{"`+tt.otherKey+`":"P1"}`)
			c := newTestClient(t, tt.flavor, srv.URL)

			id, err := c.openPIT(t.Context(), "books")
			require.Empty(t, id)
			require.EqualError(t, err, tt.label+" open point-in-time: empty pit id")
		})
	}
}

func TestOpenPITUsesTheContext(t *testing.T) {
	// A canceled context stops the request before it is sent.
	for _, tt := range flavors {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder(t)
			c := newTestClient(t, tt.flavor, rec.srv.URL)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			id, err := c.openPIT(ctx, "books")
			require.Empty(t, id)
			require.ErrorIs(t, err, context.Canceled)
			require.Empty(t, rec.method, "a canceled request is never sent")
		})
	}
}

func TestClosePITUsesTheContext(t *testing.T) {
	// closePIT returns nothing, so the server is the only evidence of the context.
	for _, tt := range flavors {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder(t)
			c := newTestClient(t, tt.flavor, rec.srv.URL)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			c.closePIT(ctx, "PIT-42")
			require.Empty(t, rec.method, "a canceled request is never sent")
		})
	}
}

func TestClosePITSurvivesAFailure(t *testing.T) {
	// The release is best effort. A refusal and a dead server do not panic.
	for _, tt := range flavors {
		t.Run(tt.name+" refusal", func(t *testing.T) {
			srv := replyServer(t, http.StatusInternalServerError, `{"error":{"type":"boom"}}`)
			c := newTestClient(t, tt.flavor, srv.URL)
			require.NotPanics(t, func() { c.closePIT(t.Context(), "PIT-42") })
		})
		t.Run(tt.name+" dead server", func(t *testing.T) {
			srv := replyServer(t, http.StatusOK, `{}`)
			addr := srv.URL
			srv.Close()
			c := newTestClient(t, tt.flavor, addr)
			require.NotPanics(t, func() { c.closePIT(t.Context(), "PIT-42") })
		})
	}
}

func TestRequestSetsTheContentType(t *testing.T) {
	// A body needs a content type. A request without a body sends none.
	rec := newRecorder(t)
	c := newTestClient(t, flavorES, rec.srv.URL)
	st := &Store{client: c, index: "books", pageSize: scanBatch}

	require.NoError(t, st.request(boundedCtx(t), "probe", http.MethodGet, "/", nil, nil))
	require.Empty(t, rec.contentType)

	require.NoError(t, st.request(boundedCtx(t), "probe", http.MethodPost, "/", []byte(`{}`), nil))
	require.Equal(t, "application/json", rec.contentType)

	_, err := st.Put(boundedCtx(t), []query.Record{{Key: "1", Value: map[string]any{"a": 1}}}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, "application/x-ndjson", rec.contentType)

	_, err = st.Delete(boundedCtx(t), []string{"1"})
	require.NoError(t, err)
	require.Equal(t, "application/x-ndjson", rec.contentType)
}

func TestNewClientTracesWithTheFlavorPrefix(t *testing.T) {
	// The trace line starts with the prefix of the flavor. Without a writer the
	// transport stays unset and the request still works.
	prefixes := map[flavor]string{flavorES: "es", flavorOS: "os"}
	for _, tt := range flavors {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder(t)
			var buf bytes.Buffer
			c, err := newClient(connConfig{flavor: tt.flavor, addr: rec.srv.URL}, &buf)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.close() })
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			require.NoError(t, err)
			res, err := c.perform(req)
			require.NoError(t, err)
			require.NoError(t, res.Body.Close())
			require.Equal(t, prefixes[tt.flavor]+"> GET /\n", buf.String())

			c2 := newTestClient(t, tt.flavor, rec.srv.URL)
			req, err = http.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			require.NoError(t, err)
			res, err = c2.perform(req)
			require.NoError(t, err)
			require.NoError(t, res.Body.Close())
		})
	}
}

func TestOperationErrorTextsAreExact(t *testing.T) {
	// The label and the operation join into one prefix. These texts are what a user
	// sees, so the join must not change them.
	for _, tt := range flavors {
		t.Run(tt.name+" open refusal", func(t *testing.T) {
			srv := replyServer(t, http.StatusNotFound, `{"error":{"type":"index_not_found_exception","reason":"no such index"}}`)
			c := newTestClient(t, tt.flavor, srv.URL)
			_, err := c.openPIT(t.Context(), "books")
			require.EqualError(t, err, tt.label+" open point-in-time: index_not_found_exception: no such index")
		})
		t.Run(tt.name+" open status only", func(t *testing.T) {
			srv := replyServer(t, http.StatusBadGateway, `<html>`)
			c := newTestClient(t, tt.flavor, srv.URL)
			_, err := c.openPIT(t.Context(), "books")
			require.EqualError(t, err, tt.label+" open point-in-time: unexpected status 502")
		})
		t.Run(tt.name+" open decode", func(t *testing.T) {
			srv := replyServer(t, http.StatusOK, `nope`)
			c := newTestClient(t, tt.flavor, srv.URL)
			_, err := c.openPIT(t.Context(), "books")
			require.ErrorContains(t, err, tt.label+" open point-in-time: decode response: ")
			require.Regexp(t, "^"+tt.label+" open point-in-time: decode response: ", err.Error())
		})
		t.Run(tt.name+" open transport", func(t *testing.T) {
			srv := replyServer(t, http.StatusOK, `{}`)
			addr := srv.URL
			srv.Close()
			c := newTestClient(t, tt.flavor, addr)
			_, err := c.openPIT(t.Context(), "books")
			require.Regexp(t, "^"+tt.label+" open point-in-time: ", err.Error())
		})
	}
	t.Run("store request", func(t *testing.T) {
		st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
			esJSON(w, http.StatusNotFound, `{}`)
		})
		err := st.request(boundedCtx(t), "probe", http.MethodGet, "/", nil, nil)
		require.EqualError(t, err, "elasticsearch probe: unexpected status 404")
	})
	t.Run("store query", func(t *testing.T) {
		st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
			esJSON(w, http.StatusBadRequest, `{"error":{"type":"parsing_exception","reason":"bad query"}}`)
		})
		_, err := st.Query(boundedCtx(t), []string{`{"match_all":{}}`})
		require.EqualError(t, err, "elasticsearch search: parsing_exception: bad query")
	})
	t.Run("store bulk", func(t *testing.T) {
		st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
			esJSON(w, http.StatusTooManyRequests, `{"error":{"type":"rejected","reason":"busy"}}`)
		})
		_, err := st.Put(boundedCtx(t), []query.Record{{Key: "1", Value: map[string]any{"a": 1}}}, query.Upsert)
		require.EqualError(t, err, "elasticsearch bulk write: rejected: busy")
		_, err = st.Delete(boundedCtx(t), []string{"1"})
		require.EqualError(t, err, "elasticsearch bulk delete: rejected: busy")
	})
}

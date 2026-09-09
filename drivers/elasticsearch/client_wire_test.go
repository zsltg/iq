package elasticsearch

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// recorder is a stub server that keeps the last request it received. The client
// tests use it because the credentials and the close-point-in-time body only
// become visible on the wire.
type recorder struct {
	srv         *httptest.Server
	auth        string
	body        string
	path        string
	method      string
	contentType string
}

// newRecorder starts a stub server that answers every request with an empty JSON
// object and records what it received.
func newRecorder(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.auth = req.Header.Get("Authorization")
		r.contentType = req.Header.Get("Content-Type")
		r.method = req.Method
		r.path = req.URL.Path
		if req.Body != nil {
			b, _ := io.ReadAll(req.Body)
			r.body = string(b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func TestNewClientSendsBasicAuth(t *testing.T) {
	// Both flavors must put the URL's credentials on every request. A config that
	// loses the user name or the password still builds a client and still reaches
	// the server, so only the Authorization header shows the difference.
	tests := []struct {
		name   string
		flavor flavor
	}{
		{name: "elasticsearch", flavor: flavorES},
		{name: "opensearch", flavor: flavorOS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder(t)
			c, err := newClient(connConfig{
				flavor:   tt.flavor,
				addr:     rec.srv.URL,
				username: "elastic",
				password: "s3cret",
			}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.close() })

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			require.NoError(t, err)
			res, err := c.perform(req)
			require.NoError(t, err)
			require.NoError(t, res.Body.Close())

			want := "Basic " + base64.StdEncoding.EncodeToString([]byte("elastic:s3cret"))
			require.Equal(t, want, rec.auth, "the request carries both the user name and the password")
		})
	}
}

func TestClosePITSendsTheIdentifier(t *testing.T) {
	// closePIT releases the point-in-time the scan opened. It ignores every error,
	// so a body that lost the identifier leaves the point-in-time open on the
	// server and no caller sees a failure. The request body is the only evidence.
	tests := []struct {
		name     string
		flavor   flavor
		wantPath string
		wantKey  string
	}{
		{name: "elasticsearch", flavor: flavorES, wantPath: "/_pit", wantKey: `"id":"PIT-42"`},
		{name: "opensearch", flavor: flavorOS, wantPath: "/_search/point_in_time", wantKey: `"pit_id":["PIT-42"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder(t)
			c, err := newClient(connConfig{flavor: tt.flavor, addr: rec.srv.URL}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.close() })

			c.closePIT(t.Context(), "PIT-42")

			require.Equal(t, http.MethodDelete, rec.method)
			require.Equal(t, tt.wantPath, rec.path)
			require.JSONEq(t, "{"+tt.wantKey+"}", rec.body, "the body names the point-in-time to release")
			require.Equal(t, "application/json", rec.contentType,
				"the server refuses a JSON body that comes with no content type")
		})
	}
}

func TestNewClientRejectsAnUnusableAddress(t *testing.T) {
	// An address that is not a URL cannot make a client. Both flavors must report the
	// failure and hand back no client, or the caller keeps a client that reaches
	// nothing and fails much later.
	tests := []struct {
		name     string
		flavor   flavor
		wantWrap string
	}{
		{name: "elasticsearch", flavor: flavorES, wantWrap: "connect elasticsearch"},
		{name: "opensearch", flavor: flavorOS, wantWrap: "connect opensearch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := newClient(connConfig{flavor: tt.flavor, addr: "://nope"}, nil)
			require.Nil(t, c, "a failed build hands back no client")
			require.ErrorContains(t, err, tt.wantWrap)
			require.ErrorContains(t, err, "missing protocol scheme")
		})
	}
}

func TestOpenPITRejectsAnUnusableIndexPath(t *testing.T) {
	// The index name becomes part of the request path. A name a URL cannot hold must
	// stop at the request build, for both flavors.
	tests := []struct {
		name   string
		flavor flavor
	}{
		{name: "elasticsearch", flavor: flavorES},
		{name: "opensearch", flavor: flavorOS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder(t)
			c, err := newClient(connConfig{flavor: tt.flavor, addr: rec.srv.URL}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.close() })

			id, err := c.openPIT(t.Context(), "bad\x7findex")
			require.Empty(t, id)
			require.ErrorContains(t, err, "invalid control character in URL")
			require.Empty(t, rec.method, "a request that cannot be built is never sent")
		})
	}
}

func TestOpenPITReportsTheServerRefusal(t *testing.T) {
	// A refused point-in-time must carry the server's reason. A guard that ignores the
	// refusal falls through to the empty-id check, which reports a different fault and
	// hides the cause.
	tests := []struct {
		name   string
		flavor flavor
		label  string
	}{
		{name: "elasticsearch", flavor: flavorES, label: "elasticsearch"},
		{name: "opensearch", flavor: flavorOS, label: "opensearch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				esJSON(w, http.StatusNotFound, `{"error":{"type":"index_not_found_exception","reason":"no such index"}}`)
			}))
			t.Cleanup(srv.Close)
			c, err := newClient(connConfig{flavor: tt.flavor, addr: srv.URL}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.close() })

			id, err := c.openPIT(t.Context(), "books")
			require.Empty(t, id)
			require.ErrorContains(t, err, tt.label+" open point-in-time: index_not_found_exception")
			require.ErrorContains(t, err, "no such index")
		})
	}
}

func TestOpenPITRejectsAnEmptyIdentifier(t *testing.T) {
	// A reply with no identifier is not a usable point-in-time, whatever its status.
	tests := []struct {
		name   string
		flavor flavor
		label  string
	}{
		{name: "elasticsearch", flavor: flavorES, label: "elasticsearch"},
		{name: "opensearch", flavor: flavorOS, label: "opensearch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				esJSON(w, http.StatusOK, `{}`)
			}))
			t.Cleanup(srv.Close)
			c, err := newClient(connConfig{flavor: tt.flavor, addr: srv.URL}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.close() })

			id, err := c.openPIT(t.Context(), "books")
			require.Empty(t, id)
			require.ErrorContains(t, err, tt.label+" open point-in-time: empty pit id")
		})
	}
}

func TestDecodeIntoWrapsTheTransportFailure(t *testing.T) {
	// A server that is not there gives a transport failure. The message must name the
	// backend and the operation, and the cause must stay reachable, so a caller can
	// tell a network fault from a refusal.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	c, err := newClient(connConfig{flavor: flavorES, addr: addr}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.close() })
	st := &Store{client: c, index: "books", pageSize: scanBatch}

	n, err := st.EstimateCount(t.Context())
	require.Zero(t, n)
	require.ErrorContains(t, err, "elasticsearch count:", "the message names the backend and the operation")
	var netErr *net.OpError
	require.ErrorAs(t, err, &netErr, "the cause stays reachable through the wrap")
}

func TestDecodeIntoReportsAMalformedBody(t *testing.T) {
	// A success status with a body that is not JSON must fail, not leave the caller
	// with the zero value and no error.
	st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
		esJSON(w, http.StatusOK, `not-json`)
	})
	n, err := st.EstimateCount(t.Context())
	require.Zero(t, n)
	require.ErrorContains(t, err, "elasticsearch count: decode response")
}

func TestAPIErrorNamesTheTypeOrTheStatus(t *testing.T) {
	// A refusal reports the type and reason the server gives. A reply that is not the
	// error envelope has no type, so the message must name the status instead of
	// reporting an empty type.
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name: "type and reason", status: http.StatusNotFound,
			body: `{"error":{"type":"index_not_found_exception","reason":"no such index"}}`,
			want: "elasticsearch count: index_not_found_exception: no such index",
		},
		{
			name: "type without a reason", status: http.StatusBadRequest,
			body: `{"error":{"type":"parsing_exception"}}`,
			want: "elasticsearch count: parsing_exception",
		},
		{
			name: "no error envelope", status: http.StatusBadRequest,
			body: `{}`,
			want: "elasticsearch count: unexpected status 400",
		},
		{
			name: "body is not json", status: http.StatusConflict,
			body: `<html>gateway</html>`,
			want: "elasticsearch count: unexpected status 409",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newStubStore(t, "books", func(w http.ResponseWriter, _ *http.Request) {
				esJSON(w, tt.status, tt.body)
			})
			n, err := st.EstimateCount(t.Context())
			require.Zero(t, n, "a failed count reports no documents")
			require.EqualError(t, err, tt.want)
		})
	}
}

func TestScanBodyKeepsThePITAlive(t *testing.T) {
	// A keyset scan pages inside one point-in-time. Each search must send the
	// keep_alive beside the identifier, or the point-in-time expires between pages
	// and a long scan fails part way through. A short scan still succeeds without
	// it, so only the request body shows the difference.
	var searchBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		switch {
		case strings.HasSuffix(req.URL.Path, "/_pit"):
			_, _ = w.Write([]byte(`{"id":"PIT-1"}`))
		case strings.HasSuffix(req.URL.Path, "/_search"):
			b, _ := io.ReadAll(req.Body)
			searchBody = string(b)
			_, _ = w.Write([]byte(`{"hits":{"hits":[]}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	c, err := newClient(connConfig{flavor: flavorES, addr: srv.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.close() })
	st := &Store{client: c, index: "books", pageSize: scanBatch}

	require.NoError(t, st.ScanBatches(t.Context(), func(map[string]any) error { return nil }))

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(searchBody), &body))
	pit, ok := body["pit"].(map[string]any)
	require.True(t, ok, "the search body carries a pit clause")
	require.Equal(t, "PIT-1", pit["id"])
	require.NotEmpty(t, pit["keep_alive"], "the pit clause holds the keep_alive that stops it expiring")
}

package elasticsearch

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// esJSON writes a JSON reply with the headers the go-elasticsearch client expects.
func esJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Elastic-Product", "Elasticsearch")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// newStubStore starts a stub server with the given handler and returns a Store that
// speaks to it. A live server answers correctly, so a stub is the only way to reach the
// failure paths: a refused read, a malformed body, or a rotated point-in-time id. Each
// test that uses it asserts what the path produces, never only that the path runs.
func newStubStore(t *testing.T, index string, h http.HandlerFunc) *Store {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := newClient(connConfig{flavor: flavorES, addr: srv.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.close() })
	return &Store{client: c, index: index, pageSize: scanBatch}
}

// pitStub answers a point-in-time keyset scan: it opens a point-in-time, hands out the
// scripted search pages in order, and keeps every body it received. The recorded bodies
// are what the scan's paging decisions become visible in.
type pitStub struct {
	pages    []string // the raw _search replies, one per page.
	searches []string // the body each search sent, in order.
	closed   string   // the body the close-point-in-time request sent.
}

func (p *pitStub) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodDelete && r.URL.Path == "/_pit":
		b, _ := io.ReadAll(r.Body)
		p.closed = string(b)
		esJSON(w, http.StatusOK, `{}`)
	case strings.HasSuffix(r.URL.Path, "/_pit"):
		esJSON(w, http.StatusOK, `{"id":"PIT-1"}`)
	case r.URL.Path == "/_search":
		b, _ := io.ReadAll(r.Body)
		p.searches = append(p.searches, string(b))
		i := min(len(p.searches)-1, len(p.pages)-1)
		esJSON(w, http.StatusOK, p.pages[i])
	default:
		esJSON(w, http.StatusOK, `{}`)
	}
}

// closeErrClient is an esClient whose close reports a failure. Both real clients hold
// nothing to release and always return nil, so only a stub shows that Store.Close
// passes the client's verdict on.
type closeErrClient struct {
	esClient
	err error
}

func (c closeErrClient) close() error { return c.err }

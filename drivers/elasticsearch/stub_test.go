package elasticsearch

import (
	"net/http"
	"net/http/httptest"
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

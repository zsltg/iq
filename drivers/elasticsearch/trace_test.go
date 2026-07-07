package elasticsearch

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTraceTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	// A nil next transport must fall back to http.DefaultTransport, so the request
	// still reaches the server and the method+path line is logged.
	rt := &traceTransport{w: &buf}
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/books/_search", nil)
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Contains(t, buf.String(), "es> GET /books/_search")
}

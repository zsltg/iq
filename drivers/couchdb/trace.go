package couchdb

import (
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/go-kivik/kivik/v4"
	kcouchdb "github.com/go-kivik/kivik/v4/couchdb"
)

// traceOption returns a kivik option that routes the driver's HTTP through a
// transport logging each request to w, backing the CLI's --verbose trace. Only the
// method and path are written — never headers — so the Authorization header that
// carries the basic-auth credential is never logged. Writes are serialized against
// tearing.
func traceOption(w io.Writer) kivik.Option {
	return kcouchdb.OptionHTTPClient(&http.Client{Transport: &traceTransport{w: w}})
}

// traceTransport logs method+path for each request, then delegates to the wrapped
// transport (http.DefaultTransport when none is set).
type traceTransport struct {
	w    io.Writer
	mu   sync.Mutex
	next http.RoundTripper
}

func (t *traceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	_, _ = fmt.Fprintf(t.w, "couch> %s %s\n", req.Method, req.URL.Path)
	t.mu.Unlock()
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(req)
}

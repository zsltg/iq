package elasticsearch

import (
	"fmt"
	"io"
	"net/http"
	"sync"
)

// traceTransport logs method+path for each request, then delegates to the wrapped
// transport (http.DefaultTransport when none is set). It backs the CLI's --verbose
// trace: only the method and path are written — never headers — so the Authorization
// header that carries the basic-auth credential is never logged. Writes are
// serialized against tearing.
type traceTransport struct {
	w    io.Writer
	mu   sync.Mutex
	next http.RoundTripper
}

func (t *traceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	_, _ = fmt.Fprintf(t.w, "es> %s %s\n", req.Method, req.URL.Path)
	t.mu.Unlock()
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(req)
}

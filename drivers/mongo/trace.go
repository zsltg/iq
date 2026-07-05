package mongo

import (
	"context"
	"fmt"
	"io"
	"sync"

	"go.mongodb.org/mongo-driver/v2/event"
)

// tracedSkip are the handshake, authentication, and session commands the driver
// issues around a query. They carry no query intent (and saslStart/saslContinue
// carry credentials), so the --verbose trace omits them and shows only the
// commands the query actually runs.
var tracedSkip = map[string]bool{
	"hello":        true,
	"ismaster":     true,
	"isMaster":     true,
	"saslStart":    true,
	"saslContinue": true,
	"authenticate": true,
	"getnonce":     true,
	"ping":         true,
	"endSessions":  true,
}

// newCommandMonitor returns a CommandMonitor that logs each query command to w,
// backing the CLI's --verbose command trace. Handshake and auth commands are
// skipped, so no credential is ever written; the find/getMore/aggregate body a
// query issues carries only its filter. Writes are serialized against tearing.
func newCommandMonitor(w io.Writer) *event.CommandMonitor {
	var mu sync.Mutex
	return &event.CommandMonitor{
		Started: func(_ context.Context, e *event.CommandStartedEvent) {
			if tracedSkip[e.CommandName] {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			_, _ = fmt.Fprintf(w, "mongo> %s %s\n", e.CommandName, e.Command.String())
		},
	}
}

package cassandra

import (
	"context"
	"fmt"
	"io"
	"sync"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
)

// queryObserver logs each executed CQL statement to a writer, backing the CLI's
// --verbose command trace. Only the statement text is written; the bound values are
// redacted, since they may carry data or a value that should not reach the log.
// Authentication is a SASL exchange, not a CQL query, so no credential is observed.
type queryObserver struct {
	mu sync.Mutex
	w  io.Writer
}

// newQueryObserver returns a query observer that traces statements to w.
func newQueryObserver(w io.Writer) *queryObserver {
	return &queryObserver{w: w}
}

// ObserveQuery writes the statement of each executed query. Writes are serialized
// against tearing, as the driver calls it from multiple goroutines.
func (o *queryObserver) ObserveQuery(_ context.Context, q gocql.ObservedQuery) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _ = fmt.Fprintf(o.w, "cql> %s\n", q.Statement)
}

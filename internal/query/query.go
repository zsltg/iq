// Package query holds the driver-agnostic core: the Store port a database
// adapter implements, and the Runner use case that forwards a query to it.
package query

import (
	"context"
	"errors"
	"fmt"
)

// ErrEmptyQuery is returned when Run is called with no command.
var ErrEmptyQuery = errors.New("empty query: expected a command")

// rowLimitKey is the context key of the row limit.
type rowLimitKey struct{}

// WithRowLimit returns a context that asks Store.Query to stop after n result
// rows. A driver that returns a list of rows reads at most n of them. It can
// read one more than the caller wants to show, so the caller can tell that the
// result is cut. A value of zero or less sets no limit, and ctx comes back as is.
// A driver that returns a single document ignores the limit.
func WithRowLimit(ctx context.Context, n int) context.Context {
	if n <= 0 {
		return ctx
	}
	return context.WithValue(ctx, rowLimitKey{}, n)
}

// RowLimit returns the row limit that WithRowLimit set on ctx. The bool is false
// when ctx has no limit.
func RowLimit(ctx context.Context) (int, bool) {
	n, ok := ctx.Value(rowLimitKey{}).(int)
	return n, ok
}

// UnderRowLimit reports whether a driver that holds n rows may read one more. It is
// true when ctx has no row limit, or when n is below the limit.
func UnderRowLimit(ctx context.Context, n int) bool {
	limit, limited := RowLimit(ctx)
	return !limited || n < limit
}

// unboundedKey is the context key of the unbounded opt-in.
type unboundedKey struct{}

// WithUnbounded returns a context that tells a driver the caller allows reads that
// load a very large value into memory. The engine sets it when the run is
// unbounded (the --unbounded flag).
func WithUnbounded(ctx context.Context) context.Context {
	return context.WithValue(ctx, unboundedKey{}, true)
}

// Unbounded reports whether WithUnbounded was applied to ctx.
func Unbounded(ctx context.Context) bool {
	on, _ := ctx.Value(unboundedKey{}).(bool)
	return on
}

// Store is the port the core depends on. A driver adapter (Redis, and later
// others) implements it. The core never imports a concrete driver.
type Store interface {
	// Query forwards the parsed command arguments to the store and returns the
	// raw result. args[0] is the command, the rest its operands.
	Query(ctx context.Context, args []string) (any, error)
	// Close releases the store's resources.
	Close() error
}

// Runner forwards a query to a Store. It exists so the forwarding policy lives
// in one place, independent of both the CLI and the driver.
type Runner struct {
	store Store
}

// NewRunner returns a Runner backed by store.
func NewRunner(store Store) *Runner {
	return &Runner{store: store}
}

// Run forwards args to the store. It rejects an empty query before touching the
// store. It wraps any store error without the statement, because the first
// argument can hold data values or credentials and the error reaches the log.
func (r *Runner) Run(ctx context.Context, args []string) (any, error) {
	if len(args) == 0 {
		return nil, ErrEmptyQuery
	}
	result, err := r.store.Query(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("run query: %w", err)
	}
	return result, nil
}

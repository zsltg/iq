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

// Store is the port the core depends on. A driver adapter (Redis, and later
// others) implements it; the core never imports a concrete driver.
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
// store, and wraps any store error with the command for context.
func (r *Runner) Run(ctx context.Context, args []string) (any, error) {
	if len(args) == 0 {
		return nil, ErrEmptyQuery
	}
	result, err := r.store.Query(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("run query %q: %w", args[0], err)
	}
	return result, nil
}

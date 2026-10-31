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
// rows. A value of zero or less sets no limit, and ctx comes back as is.
//
// A driver whose reply is a list returns up to n rows. The caller asks for one
// row more than it wants to show, so it can tell a full result from a cut one.
//
// A driver whose reply is not a list, for example a map of rows, cannot let the
// caller trim the reply. It reads at most n rows. If the n-th row exists, the
// driver drops that row, keeps n-1 rows, and calls NoteRowCut. A driver that
// returns a single document ignores the limit.
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

// MaxValueElements is the most elements, or cells per column family, that one
// stored value can hold and still be read without the unbounded opt-in. A driver
// that reads a hash, list, set, sorted set, stream, or wide row checks its value
// against this limit. One number serves all of them.
const MaxValueElements = 1_000_000

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

// rowCutKey is the context key of the row cut notice sink.
type rowCutKey struct{}

// WithRowCutNotice returns a context that carries sink. A driver whose reply is
// not a list calls NoteRowCut when the row limit stopped it before the end of
// the result.
func WithRowCutNotice(ctx context.Context, sink func()) context.Context {
	return context.WithValue(ctx, rowCutKey{}, sink)
}

// NoteRowCut tells the sink of ctx that a row limit cut the reply short. It does
// nothing when ctx has no sink.
func NoteRowCut(ctx context.Context) {
	if sink, ok := ctx.Value(rowCutKey{}).(func()); ok {
		sink()
	}
}

// CutAtLimit is for a driver whose reply is not a list. Call it after the driver
// reads a row, with n as the count of rows read so far, this one included. It
// returns true when n reaches the row limit. The driver must then drop this row
// and stop reading. CutAtLimit has already called NoteRowCut. It returns false
// when ctx has no row limit.
func CutAtLimit(ctx context.Context, n int) bool {
	limit, limited := RowLimit(ctx)
	if !limited || n < limit {
		return false
	}
	NoteRowCut(ctx)
	return true
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

package query

import (
	"context"
	"errors"
	"fmt"

	"github.com/itchyny/gojq"

	"github.com/zsltg/iq/internal/selector"
)

// ErrEmptyExpression is returned when Run is called with a blank jq expression.
var ErrEmptyExpression = errors.New("empty expression: expected a jq filter")

// ErrScanNotAllowed is returned when an expression can only be answered by
// reading the whole keyspace but the caller did not permit an unbounded read.
// The message names no flag so the core stays independent of the CLI; the caller
// translates it into whatever opt-in it exposes.
var ErrScanNotAllowed = errors.New("query requires an unbounded full-keyspace scan")

// KVStore is the port the jq use case depends on. A driver adapter implements
// it; the core never imports a concrete driver. The jq expression names the keys
// to read, so the store only ever fetches a caller-supplied, bounded set — or,
// for a bare `.`, the whole keyspace via ScanAll.
type KVStore interface {
	// Get fetches the named keys and returns a map from key to its value
	// normalized as JSON-ready Go values. A missing key maps to nil. The keys
	// are deduplicated by the caller.
	Get(ctx context.Context, keys []string) (map[string]any, error)
	// ScanAll returns every key in the store. It is the one unbounded read,
	// reached only by a bare `.` selector.
	ScanAll(ctx context.Context) ([]string, error)
	// Close releases the store's resources.
	Close() error
}

// JQEngine runs a jq expression against a KVStore. The expression is both the
// key selector and the transform: its root-level paths name the keys to fetch,
// then the same expression runs client-side over the assembled {key: value}
// object. Keeping the filter fully client-side makes the semantics identical for
// any backend behind the KVStore port.
type JQEngine struct {
	store KVStore
}

// NewJQEngine returns a JQEngine backed by store.
func NewJQEngine(store KVStore) *JQEngine {
	return &JQEngine{store: store}
}

// Run parses src, resolves the keys it references, fetches them, and runs the
// expression over the assembled root object, calling emit once per produced
// value. Results are streamed to emit rather than collected so a large output
// does not have to be buffered. When the expression can only be answered by a
// whole-keyspace scan, it runs only if allowScan is set, otherwise it returns
// ErrScanNotAllowed before touching the store. A blank expression, a parse
// failure, a store error, or a jq runtime error are all returned with context;
// the caller decides how to present them.
func (e *JQEngine) Run(ctx context.Context, src string, allowScan bool, emit func(v any) error) error {
	if src == "" {
		return ErrEmptyExpression
	}
	q, err := gojq.Parse(src)
	if err != nil {
		return fmt.Errorf("parse expression: %w", err)
	}
	keys := selector.Keys(q)
	if keys.Scan && !allowScan {
		return ErrScanNotAllowed
	}

	root, err := e.materialize(ctx, keys)
	if err != nil {
		return err
	}

	code, err := gojq.Compile(q)
	if err != nil {
		return fmt.Errorf("compile expression: %w", err)
	}
	return runCode(ctx, code, root, emit)
}

// materialize assembles the root object the expression runs against: {key:
// value} for the referenced keys, or every key when the expression needs a scan.
func (e *JQEngine) materialize(ctx context.Context, keys selector.KeySet) (map[string]any, error) {
	names := keys.Keys
	if keys.Scan {
		all, err := e.store.ScanAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("scan keys: %w", err)
		}
		names = all
	}
	root, err := e.store.Get(ctx, names)
	if err != nil {
		return nil, fmt.Errorf("fetch keys: %w", err)
	}
	return root, nil
}

// runCode drives the compiled iterator, forwarding each value to emit. gojq
// signals a runtime error by yielding an error value, which is wrapped and
// returned; the context bounds the run.
func runCode(ctx context.Context, code *gojq.Code, root map[string]any, emit func(v any) error) error {
	iter := code.RunWithContext(ctx, root)
	for {
		v, ok := iter.Next()
		if !ok {
			return nil
		}
		if err, ok := v.(error); ok {
			return fmt.Errorf("run expression: %w", err)
		}
		if err := emit(v); err != nil {
			return err
		}
	}
}

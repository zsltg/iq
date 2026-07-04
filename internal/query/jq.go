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
// materializing the whole keyspace in memory but the caller did not permit an
// unbounded read. The message names no flag so the core stays independent of the
// CLI; the caller translates it into whatever opt-in it exposes.
var ErrScanNotAllowed = errors.New("query requires materializing the whole dataset")

// KVStore is the port the jq use case depends on. A driver adapter implements
// it; the core never imports a concrete driver. The jq expression names the keys
// to read, so the store fetches a caller-supplied, bounded set with Get, or
// walks the whole keyspace in pages with ScanBatches.
type KVStore interface {
	// Get fetches the named keys and returns a map from key to its value
	// normalized as JSON-ready Go values. A missing key maps to nil. The keys
	// are deduplicated by the caller.
	Get(ctx context.Context, keys []string) (map[string]any, error)
	// ScanBatches walks the whole keyspace, invoking fn with each page of
	// {key: value} as it is fetched, so a streaming caller never holds more than
	// one page in memory. A page may repeat a key if the keyspace is resized
	// mid-scan (the store's weak scan guarantee). It stops and returns the first
	// error from fn or the store.
	ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error
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

// Run parses src, classifies the reads it needs, and runs it, calling emit once
// per produced value. It routes three ways:
//   - bounded (the filter names specific keys): fetch just those and run once.
//   - streamable scan (`.[]`-rooted) without allowUnbounded: run the filter over
//     each keyspace page and emit as it goes, so memory stays O(page).
//   - otherwise (a holistic scan, or a streamable scan with allowUnbounded set):
//     materialize the whole keyspace and run once. A holistic scan without
//     allowUnbounded is refused with ErrScanNotAllowed before touching the store.
//
// Results stream to emit rather than being collected. A blank expression, a
// parse failure, a store error, or a jq runtime error are returned with context.
func (e *JQEngine) Run(ctx context.Context, src string, allowUnbounded bool, emit func(v any) error) error {
	if src == "" {
		return ErrEmptyExpression
	}
	q, err := gojq.Parse(src)
	if err != nil {
		return fmt.Errorf("parse expression: %w", err)
	}
	keys := selector.Keys(q)

	code, err := gojq.Compile(q)
	if err != nil {
		return fmt.Errorf("compile expression: %w", err)
	}

	switch {
	case !keys.Scan:
		return e.runBounded(ctx, code, keys.Keys, emit)
	case keys.Streamable && !allowUnbounded:
		return e.runStreaming(ctx, code, emit)
	case !allowUnbounded:
		// A holistic scan has no batched form; it must materialize, which the
		// caller has not permitted.
		return ErrScanNotAllowed
	default:
		return e.runMaterialized(ctx, code, emit)
	}
}

// runBounded fetches the named keys and runs the filter once over them.
func (e *JQEngine) runBounded(ctx context.Context, code *gojq.Code, names []string, emit func(v any) error) error {
	root, err := e.store.Get(ctx, names)
	if err != nil {
		return fmt.Errorf("fetch keys: %w", err)
	}
	return runCode(ctx, code, root, emit)
}

// runStreaming runs the filter over each keyspace page and emits as it goes.
// Because the filter is `.[]`-rooted it distributes over the pages, so the
// concatenated per-page outputs equal a single run over the whole keyspace
// (modulo order). Memory stays bounded to one page.
func (e *JQEngine) runStreaming(ctx context.Context, code *gojq.Code, emit func(v any) error) error {
	err := e.store.ScanBatches(ctx, func(batch map[string]any) error {
		return runCode(ctx, code, batch, emit)
	})
	if err != nil {
		return fmt.Errorf("scan keys: %w", err)
	}
	return nil
}

// runMaterialized assembles the whole keyspace into one object and runs the
// filter over it once. It is the path for holistic filters and for a streamable
// filter the caller chose to run unbounded (for key-sorted, single-pass output).
func (e *JQEngine) runMaterialized(ctx context.Context, code *gojq.Code, emit func(v any) error) error {
	root := map[string]any{}
	err := e.store.ScanBatches(ctx, func(batch map[string]any) error {
		for k, v := range batch {
			root[k] = v
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan keys: %w", err)
	}
	return runCode(ctx, code, root, emit)
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

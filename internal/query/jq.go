package query

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/itchyny/gojq"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/pushdown"
	"github.com/zsltg/iq/internal/selector"
)

// discardLogger is the no-op logger the engine uses when RunOptions carries none,
// so every log point is nil-safe without a guard at the call site. slog is a
// stdlib type, so logging keeps the core free of any driver or CLI framework.
var discardLogger = slog.New(slog.DiscardHandler)

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

// FilteredScanner is an optional capability a KVStore may also implement: given a
// pushed-down predicate, stream only the matching documents, letting the store
// pre-filter. The predicate is a conservative superset, so the engine still re-runs
// the full filter over each page. Most implementers push the predicate to the
// backend server-side; Redis instead evaluates it client-side against each
// document's raw bytes, dropping a provable non-match before it is decoded (see
// internal/rawpred). Either way the engine's re-filter keeps results identical. A
// store that does neither simply does not implement it, and the engine falls back
// to a full scan.
type FilteredScanner interface {
	ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error
}

// Estimator is an optional capability a KVStore may implement: a cheap,
// approximate count of the items a full unfiltered scan will walk, so a CLI
// adapter can show the running scan count against a rough total. It must be
// cheap — a metadata read, not a scan or a second query pass — so a store that
// can only answer by scanning does not implement it. The count is a hint: it may
// be stale and drift as the keyspace changes mid-scan, and it is meaningful only
// for an unfiltered scan (the engine requests it only then), never a pushed-down
// filtered scan whose walked set is smaller than the whole keyspace.
type Estimator interface {
	EstimateCount(ctx context.Context) (int64, error)
}

// SourceOpener resolves a named source to a KVStore, so the in-filter
// source(name; filter) function can read from sources other than the primary.
// The CLI implements it over the source registry; the core stays driver-agnostic.
type SourceOpener interface {
	Open(ctx context.Context, name string) (KVStore, error)
}

// RunOptions carries the per-run policy flags. Unbounded permits materializing
// the whole dataset in memory; Compile asks the engine to push a filter's
// predicate to the store when it can. OnPage, when non-nil, is called once per
// scanned page with the page's item count, so a CLI adapter can report scan
// progress; the core stays UI-agnostic (it only ever calls a plain func).
type RunOptions struct {
	Unbounded bool
	Compile   bool
	OnPage    func(n int)
	// OnEstimate, when non-nil, is called at most once before an unfiltered scan
	// starts with a cheap approximate item count, when the store is an Estimator.
	// It is a best-effort hint for a progress display: it never fires for a
	// bounded read or a pushed-down filtered scan, and a store that cannot
	// estimate cheaply leaves it uncalled. The core stays UI-agnostic.
	OnEstimate func(n int64)
	// Logger, when non-nil, receives the engine's structured records: a DEBUG
	// "scan strategy" record with the runtime pushdown decision. A nil Logger is
	// treated as a discard sink, so the core is a cheap no-op when the caller did
	// not enable logging. It is the stdlib *slog.Logger, so the core stays free of
	// any driver or CLI framework.
	Logger *slog.Logger
}

// logger returns the run's logger, or the shared discard logger when none was
// set, so every log point is nil-safe without a guard at the call site.
func (o RunOptions) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return discardLogger
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
//   - streamable scan (`.[]`-rooted) without Unbounded: run the filter over each
//     keyspace page and emit as it goes, so memory stays O(page). With Compile,
//     a pushable predicate pre-filters the pages at the store.
//   - otherwise (a holistic scan, or a streamable scan with Unbounded set):
//     materialize the whole keyspace and run once. A holistic scan without
//     Unbounded is refused with ErrScanNotAllowed before touching the store.
//
// Results stream to emit rather than being collected. A blank expression, a
// parse failure, a store error, or a jq runtime error are returned with context.
func (e *JQEngine) Run(ctx context.Context, src string, opts RunOptions, emit func(v any) error) error {
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
	case keys.Streamable && !opts.Unbounded:
		scan, pushed := e.scanner(q, opts)
		// A pushed-down filter walks a subset of the keyspace, so a whole-keyspace
		// estimate would overshoot; only an unfiltered scan gets a total.
		if !pushed {
			e.estimate(ctx, opts.OnEstimate)
		}
		return e.runStreaming(ctx, code, scan, opts.OnPage, emit)
	case !opts.Unbounded:
		// A holistic scan has no batched form; it must materialize, which the
		// caller has not permitted.
		return ErrScanNotAllowed
	default:
		// The materialized path always scans the whole keyspace unfiltered.
		e.estimate(ctx, opts.OnEstimate)
		return e.runMaterialized(ctx, code, opts.OnPage, emit)
	}
}

// scanner picks how the streamable pages are produced and reports whether it
// pushed a filter down. With Compile set, a pushable predicate and a store that
// supports FilteredScanner let the store pre-filter (returns true); otherwise it
// is a plain full scan (returns false). Either way the full filter re-runs per
// page, so the choice only affects how much the store returns; the bool also
// tells Run whether a whole-keyspace estimate is a valid total for the scan.
func (e *JQEngine) scanner(q *gojq.Query, opts RunOptions) (func(context.Context, func(map[string]any) error) error, bool) {
	log := opts.logger()
	if !opts.Compile {
		log.Debug("scan strategy", "pushed", false, "reason", "pushdown disabled")
		return e.store.ScanBatches, false
	}
	fs, ok := e.store.(FilteredScanner)
	if !ok {
		log.Debug("scan strategy", "pushed", false, "reason", "store does not filter scans")
		return e.store.ScanBatches, false
	}
	pred, ok := pushdown.Compile(q)
	if !ok {
		log.Debug("scan strategy", "pushed", false, "reason", "no pushable predicate")
		return e.store.ScanBatches, false
	}
	log.Debug("scan strategy", "pushed", true, "reason", "predicate pushed to filtered scanner")
	return func(ctx context.Context, fn func(map[string]any) error) error {
		return fs.ScanFiltered(ctx, pred, fn)
	}, true
}

// estimate invokes onEstimate with a cheap approximate item count before a scan,
// when the store is an Estimator and onEstimate is set. It is best-effort: a
// store that is not an Estimator, a nil callback, or a failed estimate all leave
// the caller with no total rather than an error — a progress hint must never
// break the query it describes, so the estimate error is deliberately dropped.
func (e *JQEngine) estimate(ctx context.Context, onEstimate func(int64)) {
	if onEstimate == nil {
		return
	}
	est, ok := e.store.(Estimator)
	if !ok {
		return
	}
	n, err := est.EstimateCount(ctx)
	if err != nil {
		return
	}
	onEstimate(n)
}

// runBounded fetches the named keys and runs the filter once over them.
func (e *JQEngine) runBounded(ctx context.Context, code *gojq.Code, names []string, emit func(v any) error) error {
	root, err := e.store.Get(ctx, names)
	if err != nil {
		return fmt.Errorf("fetch keys: %w", err)
	}
	return runCode(ctx, code, root, emit)
}

// runStreaming runs the filter over each page produced by scan and emits as it
// goes. Because the filter is `.[]`-rooted it distributes over the pages, so the
// concatenated per-page outputs equal a single run over the whole keyspace
// (modulo order). Running the full filter per page is also the re-apply that
// keeps a pushed-down (superset) pre-filter correct. Memory stays O(page).
func (e *JQEngine) runStreaming(ctx context.Context, code *gojq.Code, scan func(context.Context, func(map[string]any) error) error, onPage func(int), emit func(v any) error) error {
	err := scan(ctx, func(batch map[string]any) error {
		if onPage != nil {
			onPage(len(batch))
		}
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
func (e *JQEngine) runMaterialized(ctx context.Context, code *gojq.Code, onPage func(int), emit func(v any) error) error {
	root := map[string]any{}
	err := e.store.ScanBatches(ctx, func(batch map[string]any) error {
		if onPage != nil {
			onPage(len(batch))
		}
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

package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// store is the backend a command talks to. One adapter satisfies both the jq
// port (KVStore) and the raw port (Store), plus FormatRaw for the raw reply, so
// the CLI is written once against this interface and the composition root picks
// the concrete adapter by URL scheme.
type store interface {
	query.KVStore
	query.Store
	FormatRaw(v any, colored bool) string
}

// openStore connects to the backend named by cfg.url, logging the attempt and
// the outcome at the cmd boundary (the driver is safe; the URL is never logged).
func openStore(ctx context.Context, cfg *config) (store, error) {
	cfg.log().Debug("opening store", "driver", schemeOf(cfg.url))
	st, err := dialStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cfg.log().Info("store opened", "driver", schemeOf(cfg.url))
	return wrapStoreLogging(ctx, st, cfg.log()), nil
}

// wrapStoreLogging returns st wrapped in the DEBUG port-call decorator when a
// DEBUG sink is listening, else st unchanged: with logging off the native store
// is returned, so behaviour and cost are unchanged. The wrapper preserves the
// store's optional capabilities (FilteredScanner/Estimator), so pushdown and
// estimates still work through it. The ctx gates the Enabled check.
func wrapStoreLogging(ctx context.Context, st store, lg *slog.Logger) store {
	if lg.Enabled(ctx, slog.LevelDebug) {
		return newLoggingStore(st, lg)
	}
	return st
}

// newLoggingStore wraps st so every port call is logged at DEBUG, selecting the
// narrowest wrapper that still advertises the store's optional capabilities: a
// wrapper that always claimed FilteredScanner/Estimator would defeat the engine's
// capability probe (it would push to a store that cannot filter, or estimate a
// store that cannot count). The four concrete types cover the presence or absence
// of each capability, so the wrapped store's method set matches the original's.
func newLoggingStore(st store, lg *slog.Logger) store {
	base := loggingStore{st: st, lg: lg}
	fs, hasFS := st.(query.FilteredScanner)
	est, hasEst := st.(query.Estimator)
	switch {
	case hasFS && hasEst:
		return &loggingFilterEstimator{loggingFilter{base, fs}, est}
	case hasFS:
		return &loggingFilter{base, fs}
	case hasEst:
		return &loggingEstimator{base, est}
	default:
		return &base
	}
}

// loggingStore is the base decorator: it delegates every store method to the
// wrapped store and logs the four data-path calls (Get/ScanBatches/Query, and
// ScanFiltered on the capability wrapper) at DEBUG with a duration, the item
// counts, and the error status. Close and FormatRaw pass through unlogged. It
// holds the delegate as a field rather than embedding it, so it never promotes an
// optional capability the delegate happens to satisfy — the capability wrappers
// add those explicitly.
type loggingStore struct {
	st store
	lg *slog.Logger
}

// record emits one "store call" DEBUG record for a finished port call: the op
// name, the elapsed time, the caller's op-specific counts, and the error when the
// call failed. The message is a fixed literal; every datum is an attr, the
// documented stable-key contract.
func (s *loggingStore) record(op string, start time.Time, err error, attrs ...any) {
	args := make([]any, 0, len(attrs)+6)
	args = append(args, "op", op, "elapsed", time.Since(start))
	args = append(args, attrs...)
	if err != nil {
		args = append(args, "err", err)
	}
	s.lg.Debug("store call", args...)
}

// Get delegates the bounded fetch, logging the requested and returned key
// counts. Since a missing key is absent from the result, "returned" is the
// number of keys that existed, not an echo of "keys".
func (s *loggingStore) Get(ctx context.Context, keys []string) (map[string]any, error) {
	start := time.Now()
	out, err := s.st.Get(ctx, keys)
	s.record("Get", start, err, "keys", len(keys), "returned", len(out))
	return out, err
}

// ScanBatches delegates the full scan, counting the pages and items streamed
// through it before logging the totals.
func (s *loggingStore) ScanBatches(ctx context.Context, fn func(map[string]any) error) error {
	start := time.Now()
	var pages, items int
	err := s.st.ScanBatches(ctx, func(batch map[string]any) error {
		pages++
		items += len(batch)
		return fn(batch)
	})
	s.record("ScanBatches", start, err, "pages", pages, "items", items)
	return err
}

// Query delegates the raw exec call, logging the command verb and operand count
// only — never the operands themselves, which may carry data values.
func (s *loggingStore) Query(ctx context.Context, args []string) (any, error) {
	start := time.Now()
	out, err := s.st.Query(ctx, args)
	command := ""
	operands := 0
	if len(args) > 0 {
		command = args[0]
		operands = len(args) - 1
	}
	s.record("Query", start, err, "command", command, "args", operands)
	return out, err
}

// Close delegates the resource release, unlogged.
func (s *loggingStore) Close() error { return s.st.Close() }

// FormatRaw delegates the raw-reply rendering, unlogged (it is pure formatting).
func (s *loggingStore) FormatRaw(v any, colored bool) string { return s.st.FormatRaw(v, colored) }

// loggingFilter adds the FilteredScanner capability to the base decorator,
// logging the filtered scan's page and item counts. It is used only when the
// wrapped store filters scans, so the wrapper never advertises a capability the
// delegate lacks.
type loggingFilter struct {
	loggingStore
	fs query.FilteredScanner
}

// ScanFiltered delegates the pushed-down scan, counting the pages and items the
// store returned before logging the totals.
func (s *loggingFilter) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(map[string]any) error) error {
	start := time.Now()
	var pages, items int
	err := s.fs.ScanFiltered(ctx, pred, func(batch map[string]any) error {
		pages++
		items += len(batch)
		return fn(batch)
	})
	s.record("ScanFiltered", start, err, "pages", pages, "items", items)
	return err
}

// loggingEstimator adds the Estimator capability to the base decorator. The count
// is a progress hint, not a port call, so it is delegated unlogged; the wrapper
// exists only to keep the capability visible to the engine's probe.
type loggingEstimator struct {
	loggingStore
	est query.Estimator
}

// EstimateCount delegates the cheap count, unlogged.
func (s *loggingEstimator) EstimateCount(ctx context.Context) (int64, error) {
	return s.est.EstimateCount(ctx)
}

// loggingFilterEstimator carries both optional capabilities, for a store that is
// a FilteredScanner and an Estimator (most live backends). It embeds the filtered
// wrapper for ScanFiltered and adds EstimateCount.
type loggingFilterEstimator struct {
	loggingFilter
	est query.Estimator
}

// EstimateCount delegates the cheap count, unlogged.
func (s *loggingFilterEstimator) EstimateCount(ctx context.Context) (int64, error) {
	return s.est.EstimateCount(ctx)
}

// dialStore connects to the backend named by cfg.url, dispatching on the URL
// scheme through the drivers registry — the single source of truth for which
// concrete adapters exist.
func dialStore(ctx context.Context, cfg *config) (store, error) {
	scheme := schemeOf(cfg.url)
	if scheme == "" {
		return nil, fmt.Errorf("missing URI scheme in %q; %s", redactURL(cfg.url), expectedSchemes())
	}
	d, ok := driverForScheme(scheme)
	if !ok {
		return nil, fmt.Errorf("unsupported URI scheme %q; %s", scheme, expectedSchemes())
	}
	return d.open(ctx, cfg)
}

// supportedScheme reports whether url's scheme is one openStore can dispatch. It
// is the single check `iq add` uses to reject a source the CLI cannot open.
func supportedScheme(url string) bool {
	_, ok := driverForScheme(schemeOf(url))
	return ok
}

// schemeOf returns the lowercased scheme of a connection URL — the text before
// "://" — or "" if there is none. It does not use net/url so that a multi-host
// MongoDB URI (mongodb://h1,h2/db), which net/url rejects, still dispatches.
func schemeOf(raw string) string {
	before, _, found := strings.Cut(raw, "://")
	if !found {
		return ""
	}
	return strings.ToLower(before)
}

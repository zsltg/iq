package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	iqfile "github.com/zsltg/iq/drivers/file"
	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
)

// openJQStore opens the store the jq engine reads: the piped-stdin dump when no
// source was selected (sq-style), otherwise the resolved backend. The piped dump
// is a stream that the run reads once.
func openJQStore(cmd *cobra.Command, ctx context.Context, cfg *config) (store, error) {
	if !cfg.stdin {
		return openStore(ctx, cfg)
	}
	format := iqfile.FormatUnknown
	if cfg.fromFormat != "" {
		var err error
		if format, err = iqfile.ParseFormat(cfg.fromFormat); err != nil {
			return nil, err
		}
	}
	st, err := iqfile.OpenPipe(cmd.InOrStdin(), format, cfg.decimalMode)
	if err != nil {
		return nil, err
	}
	return st, nil
}

// runJQ is the body of the default command. It runs the jq filter through the
// engine and renders each produced value with the formatter of the selected format.
// A filter that calls source() reads entirely from named sources over a null input
// (cross-source, no primary store). Any other filter runs against the selected
// source. Formatting lives here, in the CLI adapter, so the core stays free of any
// output format.
func runJQ(cmd *cobra.Command, cfg *config, filter string) error {
	cross, err := query.UsesSource(filter)
	if err != nil {
		// UsesSource parses the filter, so this is the first place a syntax error
		// surfaces. Enrich it so --error.format.text.verbose can draw the caret.
		return asSyntaxError(filter, err)
	}

	// Resolve the output format before any source work, so conflicting format
	// flags fail fast and not after a source is resolved. The runCombine function
	// already orders it this way. The two entry points must reject the same flags
	// alike.
	fm, err := selectFormat(cfg)
	if err != nil {
		return err
	}
	// Refuse a binary format on an interactive terminal before any store I/O,
	// checking the raw stdout ahead of the progress-meter wrapping below.
	if err := guardBinaryFormat(fm, cmd.OutOrStdout()); err != nil {
		return err
	}
	if err := validateNativeQuery(cfg, fm, cross); err != nil {
		return err
	}

	// A single-source filter resolves its source up front (no connection), so both
	// the query plan and the execution below name and dispatch the same driver. When
	// no source is selected and stdin is piped, the query reads that stdin (sq-style).
	if !cross {
		if err := resolveSource(cfg); err != nil {
			if !errors.Is(err, errNoSource) || stdinIsTerminal() {
				return err
			}
			cfg.stdin = true
		}
	}

	// --explain prints the plan to stdout and stops before any connection. --verbose
	// prints it to stderr and turns on the live command trace for the run below.
	if (cfg.explain || cfg.verbose) && cfg.stdin {
		plan := planHeader("query plan") + "\n  decode stdin dump\n  scan client-side\n"
		if cfg.explain {
			_, _ = fmt.Fprint(cmd.OutOrStdout(), plan)
			return nil
		}
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), plan)
	} else if cfg.explain || cfg.verbose {
		plan, err := buildQueryPlan(cfg, filter, cross, cfg.verbose)
		if err != nil {
			return err
		}
		if cfg.explain {
			_, _ = fmt.Fprint(cmd.OutOrStdout(), plan)
			return nil
		}
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), plan)
		cfg.trace = cmd.ErrOrStderr()
	}

	// When logging is active, tee the driver command trace into the log and emit
	// the structured query plan. So a --log run captures both without --explain/-v.
	cfg.trace = traceSink(cfg.trace, cfg.log())
	cfg.logQueryPlan(filter, cross)

	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
	defer cancel()

	// The scan-progress spinner renders on stderr. Wrapping stdout lets it hold its
	// repaint while rows stream, so the two never collide. A nil meter (progress
	// off, or stderr not a terminal) makes every call below a no-op. --verbose
	// disables it, because the command trace shares stderr and is the progress
	// signal.
	meter := newProgressMeter(cmd.ErrOrStderr(), cfg.noProgress || cfg.verbose)
	defer meter.Stop()
	f := newFormatter(fm, meter.wrapStdout(cmd.OutOrStdout()), cfg.compact)
	// One OnPage closure drives both the spinner and the scanned-count log point,
	// so the core stays UI-agnostic (it only ever calls a plain func).
	var scanned int
	opts := query.RunOptions{Unbounded: cfg.unbounded, Compile: !cfg.noCompile, Logger: cfg.log(), OnPage: func(n int) {
		scanned += n
		meter.Tick(n)
	}}

	cfg.log().Debug("query start", "cross", cross, "timeout", cfg.timeout)
	start := time.Now()

	if cross {
		cf, err := iqconfig.Load()
		if err != nil {
			return err
		}
		opener := newSourceOpener(cf, cfg.trace, cfg.decimalMode, cfg.noCache, cfg.noCacheIndex)
		defer opener.closeAll()
		runErr := query.NewCrossEngine(opener).Run(ctx, filter, opts, f.emit)
		cfg.logQueryComplete(scanned, start)
		return finish(f, scanHint(asSyntaxError(filter, runErr)))
	}

	store, err := openJQStore(cmd, ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	// Only the single-source scan gets a total. It walks one keyspace, so a
	// cheap estimate from the backend is a valid denominator for the spinner.
	// The cross-source path aggregates several scans, where a per-source
	// estimate misleads. So the estimate is left off there.
	opts.OnEstimate = meter.SetEstimate
	kv, err := kvStoreFor(store, cfg)
	if err != nil {
		return err
	}
	runErr := query.NewJQEngine(kv).Run(ctx, filter, opts, f.emit)
	cfg.logQueryComplete(scanned, start)
	return finish(f, scanHint(asSyntaxError(filter, runErr)))
}

// logQueryComplete records a finished query run with the number of items scanned
// and the wall-clock elapsed, at INFO so -v surfaces it.
func (cfg *config) logQueryComplete(scanned int, start time.Time) {
	cfg.log().Info("query complete", "scanned", scanned, "elapsed", time.Since(start))
}

// logQueryPlan emits the structured "query plan" record at INFO when the
// structured file/stream sink is active. So a CI run captures the
// classification, planned ops, pushed filter, and per-conjunct decisions that
// --explain shows. It is gated on the structured sink, not the verbose sink. A
// bare -v already prints the pretty plan to stderr, so the record renders it
// twice. It shares buildSourcePlan with the --explain text, so the two never
// drift. A cross-source source() filter has no single driver, and a stdin dump
// has no backend, so those log a minimal plan. It skips building the plan when
// the structured sink is off or below INFO.
func (cfg *config) logQueryPlan(filter string, cross bool) {
	if !cfg.logStructured {
		return
	}
	lg := cfg.log()
	if !lg.Enabled(context.Background(), slog.LevelInfo) {
		return
	}
	switch {
	case cross:
		lg.Info("query plan", "mode", "cross-source")
	case cfg.stdin:
		lg.Info("query plan", "driver", "stdin",
			"ops", []string{"decode stdin dump", "scan client-side"})
	default:
		sp, err := buildSourcePlan(cfg.planSource(), filter, !cfg.noCompile, cfg.unbounded)
		if err != nil || !sp.hasPlan {
			return
		}
		lg.Info("query plan", sp.logAttrs(cfg.handle)...)
	}
}

// finish returns the run error of the engine if any. Otherwise it flushes
// the formatter, which closes jsona and yaml documents that the last emit
// left open.
func finish(f formatter, runErr error) error {
	if runErr != nil {
		return runErr
	}
	return f.flush()
}

// scanHint translates the core's flag-agnostic scan refusal into this CLI's
// opt-in, leaving every other error untouched.
func scanHint(err error) error {
	if errors.Is(err, query.ErrScanNotAllowed) {
		return fmt.Errorf("%w; re-run with --unbounded, narrow it to specific keys, or use a .[]-rooted filter to stream", err)
	}
	return err
}

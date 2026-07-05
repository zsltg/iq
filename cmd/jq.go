package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
)

// runJQ is the default command's body: it runs the jq filter through the engine
// and renders each produced value with the selected-format formatter. A filter that
// calls source() reads entirely from named sources over a null input
// (cross-source, no primary store); any other filter runs against the selected
// source. Formatting lives here, in the CLI adapter, so the core stays free of
// any output format.
func runJQ(cmd *cobra.Command, cfg *config, filter string) error {
	cross, err := query.UsesSource(filter)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
	defer cancel()

	fm, err := selectFormat(cfg)
	if err != nil {
		return err
	}
	// The scan-progress spinner renders on stderr; wrapping stdout lets it hold
	// its repaint while rows stream, so the two never collide. A nil meter (progress
	// off, or stderr not a terminal) makes every call below a no-op.
	meter := newProgressMeter(cmd.ErrOrStderr(), cfg.noProgress)
	defer meter.Stop()
	f := newFormatter(fm, meter.wrapStdout(cmd.OutOrStdout()), cfg.compact)
	opts := query.RunOptions{Unbounded: cfg.unbounded, Compile: cfg.compile, OnPage: meter.Tick}

	if cross {
		cf, err := iqconfig.Load()
		if err != nil {
			return err
		}
		opener := newSourceOpener(cf)
		defer opener.closeAll()
		return finish(f, scanHint(query.NewCrossEngine(opener).Run(ctx, filter, opts, f.emit)))
	}

	if err := resolveSource(cmd, cfg); err != nil {
		return err
	}
	store, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	return finish(f, scanHint(query.NewJQEngine(store).Run(ctx, filter, opts, f.emit)))
}

// finish returns the engine's run error if any; otherwise it flushes the
// formatter, closing json-array and yaml documents that the last emit left open.
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

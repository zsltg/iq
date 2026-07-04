package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
)

// runJQ is the default command's body: it runs the jq filter through the engine
// and prints each produced value as JSON. A filter that calls source() reads
// entirely from named sources over a null input (cross-source, no primary
// store); any other filter runs against the selected source. Marshaling lives
// here, in the CLI adapter, so the core stays free of any output format.
func runJQ(cmd *cobra.Command, cfg *config, filter string) error {
	cross, err := query.UsesSource(filter)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
	defer cancel()

	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	// Preserve <, >, and & verbatim; the output is a terminal, not HTML.
	enc.SetEscapeHTML(false)
	emit := func(v any) error {
		if err := enc.Encode(v); err != nil {
			return fmt.Errorf("encode result: %w", err)
		}
		return nil
	}
	opts := query.RunOptions{Unbounded: cfg.unbounded, Compile: cfg.compile}

	if cross {
		cf, err := iqconfig.Load()
		if err != nil {
			return err
		}
		opener := newSourceOpener(cf)
		defer opener.closeAll()
		return scanHint(query.NewCrossEngine(opener).Run(ctx, filter, opts, emit))
	}

	if err := resolveSource(cmd, cfg); err != nil {
		return err
	}
	store, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	return scanHint(query.NewJQEngine(store).Run(ctx, filter, opts, emit))
}

// scanHint translates the core's flag-agnostic scan refusal into this CLI's
// opt-in, leaving every other error untouched.
func scanHint(err error) error {
	if errors.Is(err, query.ErrScanNotAllowed) {
		return fmt.Errorf("%w; re-run with --unbounded, narrow it to specific keys, or use a .[]-rooted filter to stream", err)
	}
	return err
}

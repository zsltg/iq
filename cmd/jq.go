package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zsltg/iq/internal/query"
)

// runJQ is the default command's body: it opens the store, runs the jq filter
// through the engine, and prints each produced value as JSON. Marshaling lives
// here, in the CLI adapter, so the core stays free of any output format.
func runJQ(cmd *cobra.Command, cfg *config, filter string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
	defer cancel()

	store, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	// Preserve <, >, and & verbatim; the output is a terminal, not HTML.
	enc.SetEscapeHTML(false)

	opts := query.RunOptions{Unbounded: cfg.unbounded, Compile: cfg.compile}
	err = query.NewJQEngine(store).Run(ctx, filter, opts, func(v any) error {
		if err := enc.Encode(v); err != nil {
			return fmt.Errorf("encode result: %w", err)
		}
		return nil
	})
	if errors.Is(err, query.ErrScanNotAllowed) {
		// Translate the core's flag-agnostic refusal into this CLI's opt-in.
		return fmt.Errorf("%w; re-run with --unbounded, narrow it to specific keys, or use a .[]-rooted filter to stream", err)
	}
	return err
}

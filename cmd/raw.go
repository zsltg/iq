package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zsltg/iq/internal/query"
)

// newRawCmd builds the `iq raw` subcommand: an escape hatch that forwards a
// command verbatim to the database and prints the reply. It exists for the
// writes, administration, and server-side queries the jq read path does not
// cover. What it accepts depends on the backend: for Redis a command and its
// operands (`iq raw HGETALL book:2`); for MongoDB a single JSON command document
// run with runCommand (`iq raw '{"find":"books","filter":{...}}'`).
func newRawCmd(cfg *config) *cobra.Command {
	c := &cobra.Command{
		Use:   "raw <command> [args...]",
		Short: "Forward a command to the database verbatim and print the reply",
		Long: "Forward a command to the backend verbatim. For Redis, a command and operands:\n" +
			"`iq raw SET greeting hello`, `iq raw HGETALL book:2`. For MongoDB, one JSON\n" +
			"command document run with runCommand: `iq raw '{\"find\":\"books\",\"filter\":{}}'`.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
			defer cancel()

			store, err := openStore(ctx, cfg)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			result, err := query.NewRunner(store).Run(ctx, args)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), store.FormatRaw(result)); err != nil {
				return err
			}
			return nil
		},
	}
	// Redis operands may start with a dash (negative indices such as `-1`), so
	// stop flag parsing at the first positional and forward the rest verbatim.
	c.Flags().SetInterspersed(false)
	return c
}

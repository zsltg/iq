package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zsltg/iq/internal/query"
	iqredis "github.com/zsltg/iq/internal/redis"
)

// newRawCmd builds the `iq raw` subcommand: an escape hatch that forwards a
// command verbatim to the database and prints the reply in redis-cli style. It
// exists for the writes, administration, and seeding the jq read path does not
// cover.
func newRawCmd(cfg *config) *cobra.Command {
	c := &cobra.Command{
		Use:   "raw <command> [args...]",
		Short: "Forward a command to the database verbatim and print the reply",
		Long:  "Forward a command to Redis verbatim, for example `iq raw SET greeting hello` or `iq raw GET greeting`.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
			defer cancel()

			store, err := iqredis.Open(ctx, cfg.url)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			result, err := query.NewRunner(store).Run(ctx, args)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), iqredis.FormatReply(result)); err != nil {
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

package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zsltg/iq/internal/query"
	iqredis "github.com/zsltg/iq/internal/redis"
)

// newQueryCmd builds the `iq query` subcommand.
func newQueryCmd(cfg *config) *cobra.Command {
	c := &cobra.Command{
		Use:   "query <command> [args...]",
		Short: "Forward a query to the database and print the result",
		Long:  "Forward a command to Redis, for example `iq query SET greeting hello` or `iq query GET greeting`.",
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

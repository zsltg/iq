// Package cmd wires the CLI (Cobra) to the query core. Commands are humble
// adapters: they parse flags, build a store, delegate to the core, and format.
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// config holds the connection settings shared by subcommands.
type config struct {
	url       string
	timeout   time.Duration
	unbounded bool
}

// defaultURL returns the connection URL from IQ_REDIS_URL, or a local default.
func defaultURL() string {
	if url := os.Getenv("IQ_REDIS_URL"); url != "" {
		return url
	}
	return "redis://localhost:6379/0"
}

// newRootCmd builds the root command and its subcommands. The default action is
// the jq query: a bare `iq '<filter>'` runs the filter, whose top-level paths
// name the keys to fetch. The `raw` subcommand forwards a command verbatim.
func newRootCmd() *cobra.Command {
	cfg := &config{}
	root := &cobra.Command{
		Use:   "iq <jq-filter>",
		Short: "Query NoSQL databases with jq from the command line",
		Long: "iq runs a jq filter against a NoSQL database. The filter's top-level paths\n" +
			"name the keys to fetch, for example `iq '.greeting'`, `iq '.[\"book:1\"]'` for a\n" +
			"key with a colon, or `iq '[ .a, .b ]'`. A filter that reads the whole dataset\n" +
			"(`.`, `.[]`, `keys`, `map(...)`) is an unbounded scan and runs only with\n" +
			"--unbounded. Always single-quote the filter so the shell does not expand its\n" +
			"brackets, spaces, or pipes. Redis is the first supported backend.",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A bare `iq` with no filter prints help rather than erroring, so the
			// entry point is discoverable.
			if len(args) == 0 {
				return cmd.Help()
			}
			return runJQ(cmd, cfg, args[0])
		},
	}
	root.PersistentFlags().StringVarP(&cfg.url, "url", "u", defaultURL(), "database connection URL (redis://...); overrides IQ_REDIS_URL")
	root.PersistentFlags().DurationVar(&cfg.timeout, "timeout", 5*time.Second, "per-query timeout")
	// --unbounded is local to the default jq action; the raw command never scans.
	root.Flags().BoolVar(&cfg.unbounded, "unbounded", false, "permit a filter that reads the whole keyspace (an unbounded scan)")
	root.AddCommand(newRawCmd(cfg))
	return root
}

// Execute runs the CLI. It is the composition root: it builds a signal-aware
// context, runs the root command, and maps any error to a stderr line and a
// non-zero exit code.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "iq:", err)
		os.Exit(1)
	}
}

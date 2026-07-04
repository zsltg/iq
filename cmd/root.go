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
	url     string
	timeout time.Duration
}

// defaultURL returns the connection URL from IQ_REDIS_URL, or a local default.
func defaultURL() string {
	if url := os.Getenv("IQ_REDIS_URL"); url != "" {
		return url
	}
	return "redis://localhost:6379/0"
}

// newRootCmd builds the root command and its subcommands.
func newRootCmd() *cobra.Command {
	cfg := &config{}
	root := &cobra.Command{
		Use:           "iq",
		Short:         "Query NoSQL databases from the command line",
		Long:          "iq forwards queries to NoSQL databases. Redis is the first supported backend.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&cfg.url, "url", "u", defaultURL(), "database connection URL (redis://...); overrides IQ_REDIS_URL")
	root.PersistentFlags().DurationVar(&cfg.timeout, "timeout", 5*time.Second, "per-query timeout")
	root.AddCommand(newQueryCmd(cfg))
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

// Package cmd wires the CLI (Cobra) to the query core. Commands are humble
// adapters: they parse flags, resolve a saved source, build a store, delegate to
// the core, and format.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// config holds the per-invocation settings. url, source, and handle are not
// flags: they are resolved from the selected source (--src or the active source)
// before the store opens. source and handle carry the stored source so a command
// can render its location honouring --reveal/--expand.
type config struct {
	src        string
	url        string
	source     iqconfig.Source
	handle     string
	collection string
	timeout    time.Duration
	unbounded  bool
	compile    bool
	from       []string
	combine    string
	format     string
	compact    bool
	monochrome bool
	forceColor bool
	noProgress bool
	reveal     bool
	expand     bool
}

// newRootCmd builds the root command and its subcommands. The default action is
// the jq query: a bare `iq '<filter>'` runs the filter against the active source,
// whose top-level paths name the keys to fetch. The `add`/`ls`/`rm`/`src`/`group`
// subcommands manage saved sources; `exec` forwards a command verbatim.
func newRootCmd() *cobra.Command {
	cfg := &config{}
	root := &cobra.Command{
		Use:     "iq <jq-filter>",
		Version: buildVersion(),
		Short:   "Query NoSQL databases with jq from the command line",
		Long: "iq runs a jq filter against a NoSQL database. The filter's top-level paths\n" +
			"name the keys to fetch, for example `iq '.greeting'`, `iq '.[\"book:1\"]'` for a\n" +
			"key with a colon, or `iq '[ .a, .b ]'`. A `.[]`-rooted filter (`iq '.[] |\n" +
			"select(.year)'`) streams over the whole keyspace in constant memory. A filter\n" +
			"that collapses the dataset into one value (`.`, `keys`, `map(...)`) must load it\n" +
			"all into memory and runs only with --unbounded. Always single-quote the filter\n" +
			"so the shell does not expand its brackets, spaces, or pipes.\n" +
			"\n" +
			"The database is a saved source: register connections with `iq add <name> <url>`,\n" +
			"choose a default with `iq src <name>`, and list them with `iq ls`. The backend is\n" +
			"chosen by the source's URL scheme: redis:// (key = Redis key) or mongodb:// (key =\n" +
			"document _id in the source's collection). Select a source for one run with --src.\n" +
			"\n" +
			"--compile pushes these select(...) clauses to MongoDB (results are unchanged; the\n" +
			"full jq always re-runs, so a pushed filter is only a pre-filter):\n" +
			"  .a == x                    equality (number, string, bool, null)\n" +
			"  .a == 1 or .a == 2         same-field equality-or -> $in\n" +
			"  .a >  >=  <  <=  n|\"s\"      ranges, preserving jq's cross-type ordering\n" +
			"  .a | test(\"re\")            portable regex (i/m/s flags)\n" +
			"  has(\"a\"),  .a | has(\"k\")   key presence -> $exists\n" +
			"  .a | length == n           -> $size (with type guards)\n" +
			"  .a | any(cond)             array element match -> $elemMatch\n" +
			"  E1 and E2,  E1 or E2       combine the above\n" +
			"Not pushed (run client-side): != , ranges vs bool/null, non-portable regex,\n" +
			"everything else. On Redis, or with no pushable clause, --compile is a no-op.",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		// PersistentPreRunE runs for the root action and every subcommand (none
		// override it), so the color decision is made once, at one place, for the
		// whole invocation.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if cfg.monochrome && cfg.forceColor {
				return errors.New("cannot use --monochrome with --color")
			}
			resolveColor(cfg.monochrome, cfg.forceColor, cmd.OutOrStdout())
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// A cross-source query (--from/--combine) reads several sources and
			// combines them; a plain positional filter runs against one source.
			if len(cfg.from) > 0 || cfg.combine != "" {
				if len(args) > 0 {
					return errors.New("cannot use a positional filter with --from/--combine; put the final program in --combine")
				}
				return runCombine(cmd, cfg)
			}
			// A bare `iq` with no filter prints help rather than erroring, so the
			// entry point is discoverable.
			if len(args) == 0 {
				return cmd.Help()
			}
			return runJQ(cmd, cfg, args[0])
		},
	}
	root.PersistentFlags().StringVarP(&cfg.src, "src", "s", "", "run against this saved source for one invocation (overrides the active source; see `iq src`)")
	root.PersistentFlags().StringVarP(&cfg.collection, "collection", "c", "", "MongoDB collection, overriding the source's (ignored for Redis)")
	root.PersistentFlags().DurationVar(&cfg.timeout, "timeout", 5*time.Second, "per-query timeout")
	root.PersistentFlags().BoolVarP(&cfg.monochrome, "monochrome", "M", false, "disable colored output (also honored via NO_COLOR); color is on by default only when writing to a terminal")
	root.PersistentFlags().BoolVarP(&cfg.forceColor, "color", "C", false, "force colored output even when the destination is not a terminal (e.g. a pager)")
	root.PersistentFlags().BoolVar(&cfg.noProgress, "no-progress", false, "disable the scan progress spinner (shown on stderr for long scans when it is a terminal)")
	// --unbounded and --compile are local to the default jq action.
	root.Flags().BoolVar(&cfg.unbounded, "unbounded", false, "permit a filter that loads the whole dataset into memory (also materializes a .[]-rooted filter instead of streaming it)")
	root.Flags().BoolVar(&cfg.compile, "compile", false, "push a .[]|select(...) equality predicate to the store to pre-filter server-side (MongoDB; no-op elsewhere; results are unchanged)")
	root.Flags().StringArrayVar(&cfg.from, "from", nil, "cross-source stage `name=<jq>`: reduce source name with <jq> and bind its results to $name (repeatable; needs --combine)")
	root.Flags().StringVar(&cfg.combine, "combine", "", "final jq over the --from results (each bound to $name), run over a null input")
	root.Flags().StringVarP(&cfg.format, "format", "o", "json", "output format: json (pretty stream), jsonl (compact, one per line), json-array (single [ ... ] doc), values (unquoted scalars), yaml")
	root.Flags().BoolVar(&cfg.compact, "compact", false, "collapse pretty json / json-array output to single-line (no-op for jsonl, values, yaml)")
	root.AddCommand(
		newExecCmd(cfg),
		newAddCmd(),
		newLsCmd(cfg),
		newRmCmd(),
		newMvCmd(),
		newSrcCmd(),
		newGroupCmd(),
		newPingCmd(cfg),
		newInspectCmd(cfg),
		newDiffCmd(cfg),
		newVersionCmd(),
	)
	return root
}

// Execute runs the CLI. It is the composition root: it builds a signal-aware
// context, runs the root command, and maps any error to a stderr line and a
// non-zero exit code.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		// errQuietExit (diff --exit-code) signals a non-zero status with no message:
		// the differences are the report, not an error.
		if !errors.Is(err, errQuietExit) {
			_, _ = fmt.Fprintln(os.Stderr, "iq:", err)
		}
		os.Exit(1)
	}
}

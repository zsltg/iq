// Package cmd wires the CLI (Cobra) to the query core. Commands are humble
// adapters: they parse flags, resolve a saved source, build a store, delegate to
// the core, and format.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/numfmt"
)

// config holds the per-invocation settings. url, source, and handle are not
// flags: they are resolved from the selected source (--src or the active source)
// before the store opens. source and handle carry the stored source so a command
// can render its location honouring --reveal/--expand.
type config struct {
	src        string
	configPath string
	url        string
	source     iqconfig.Source
	handle     string
	collection string
	timeout    time.Duration
	unbounded  bool
	noCompile  bool
	explain    bool
	from       []string
	combine    string
	format     string
	json       bool
	jsonArray  bool
	jsonl      bool
	yaml       bool
	raw        bool
	compact    bool
	// decimal is the raw --format.decimal flag; decimalMode is it resolved once in
	// PersistentPreRunE and passed as a plain type into the driver adapters.
	decimal     string
	decimalMode numfmt.DecimalMode
	monochrome  bool
	forceColor  bool
	noProgress  bool
	reveal      bool
	expand      bool
	// output, when non-empty, redirects the command's stdout to this file (sq's
	// -o); progress and errors still go to stderr. outClose closes that file in
	// Execute's finalize and is nil when --output is unset.
	output   string
	outClose func() error
	// Diagnostics flags, adopted from sq: a global verbose stderr mode, a
	// file-logging family, error-rendering controls, and a profiling mode.
	verbose          bool
	logEnable        bool
	logFile          string
	logLevel         string
	logFormat        string
	errorFormat      string
	errorStack       bool
	errorTextVerbose bool
	pprofMode        string
	// Finalize handles resolved once in PersistentPreRunE and consumed by
	// Execute after the command returns. logger is never nil (a discard logger
	// when every sink is off); logClose and pprofStop are nil when unused.
	logger    *slog.Logger
	logClose  func() error
	pprofStop func()
	// trace, when non-nil, is where a driver writes its live command trace (set
	// to stderr for the duration of a --verbose run); nil disables tracing.
	trace io.Writer
}

// newRootCmd builds the root command and its subcommands. The default action is
// the jq query: a bare `iq '<filter>'` runs the filter against the active source,
// whose top-level paths name the keys to fetch. The `add`/`ls`/`rm`/`src`/`group`
// subcommands manage saved sources; `exec` forwards a command verbatim.
func newRootCmd() (*cobra.Command, *config) {
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
			"iq pushes these select(...) clauses to MongoDB by default (results are unchanged;\n" +
			"the full jq always re-runs, so a pushed filter is only a pre-filter); pass\n" +
			"--no-compile to force the whole filter client-side:\n" +
			"  .a == x                    equality (number, string, bool, null)\n" +
			"  .a == 1 or .a == 2         same-field equality-or -> $in\n" +
			"  .a >  >=  <  <=  n|\"s\"      ranges, preserving jq's cross-type ordering\n" +
			"  .a | test(\"re\")            portable regex (i/m/s flags)\n" +
			"  has(\"a\"),  .a | has(\"k\")   key presence -> $exists\n" +
			"  .a | length == n           -> $size (with type guards)\n" +
			"  .a | any(cond)             array element match -> $elemMatch\n" +
			"  E1 and E2,  E1 or E2       combine the above\n" +
			"Not pushed (run client-side): != , ranges vs bool/null, non-portable regex,\n" +
			"everything else. On Redis, or with no pushable clause, pushdown is a no-op.",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		// PersistentPreRunE runs for the root action and every subcommand (none
		// override it), so the color decision and the diagnostics setup (logging,
		// error format, profiling) are made once, in one place, for the whole
		// invocation. All values are validated before any resource opens, so a bad
		// flag fails fast; resources that do open are stored on cfg immediately so
		// Execute's finalize closes them on every path.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// An explicit --config redirects the config path for the whole run by
			// setting IQ_CONFIG, the single mechanism every Load/Save already honors
			// (and a child editor inherits it). Precedence: flag > env > default.
			if cfg.configPath != "" {
				if err := os.Setenv(iqconfig.EnvConfig, cfg.configPath); err != nil {
					return fmt.Errorf("apply --config: %w", err)
				}
			}
			// Merge stored option defaults into the flags before anything reads them,
			// so a saved default (base or per-source) fills any flag left unset.
			if err := applyStoredOptions(cmd, cfg); err != nil {
				return err
			}
			if cfg.monochrome && cfg.forceColor {
				return errors.New("cannot use --monochrome with --color")
			}
			resolveColor(cfg.monochrome, cfg.forceColor, cmd.OutOrStdout())
			// --output redirects every command's stdout to a file. Resolve color
			// again against the file so a regular file drops color (unless -C forces
			// it), matching a pipe; Execute closes the file on every exit path.
			if cfg.output != "" {
				f, err := openOutputFile(cfg.output)
				if err != nil {
					return err
				}
				cmd.SetOut(f)
				cfg.outClose = f.Close
				resolveColor(cfg.monochrome, cfg.forceColor, f)
			}
			if err := validateErrorFormat(cfg.errorFormat); err != nil {
				return err
			}
			if err := validateFormat(cfg.format); err != nil {
				return err
			}
			mode, err := numfmt.ParseDecimalMode(cfg.decimal)
			if err != nil {
				return err
			}
			cfg.decimalMode = mode
			logOpts, err := resolveLogOptions(cmd, cfg)
			if err != nil {
				return err
			}
			logger, closeLog, err := logOpts.build(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			cfg.logger = logger
			cfg.logClose = closeLog
			stop, err := startProfile(cfg.pprofMode, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			cfg.pprofStop = stop
			cfg.logger.Debug("iq start", "version", buildVersion(), "cmd", cmd.CommandPath())
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
	root.PersistentFlags().StringVar(&cfg.configPath, "config", "", "path to the config file (overrides $IQ_CONFIG; default <user config dir>/iq/iq.toml)")
	root.PersistentFlags().StringVarP(&cfg.collection, "collection", "c", "", "MongoDB collection, overriding the source's (ignored for Redis)")
	root.PersistentFlags().DurationVar(&cfg.timeout, "timeout", 5*time.Second, "per-query timeout")
	root.PersistentFlags().BoolVarP(&cfg.monochrome, "monochrome", "M", false, "disable colored output (also honored via NO_COLOR); color is on by default only when writing to a terminal")
	root.PersistentFlags().BoolVarP(&cfg.forceColor, "color", "C", false, "force colored output even when the destination is not a terminal (e.g. a pager)")
	root.PersistentFlags().BoolVar(&cfg.noProgress, "no-progress", false, "disable the scan progress spinner (shown on stderr for long scans when it is a terminal)")
	// --output (sq's -o) is persistent so it redirects every command's stdout to a
	// file; progress and errors stay on stderr, and color turns off for the file.
	root.PersistentFlags().StringVarP(&cfg.output, "output", "o", "", "write output to <file> instead of stdout (color off unless -C; progress and errors still go to stderr)")
	// --format.decimal is persistent so it reaches every store the drivers open; it
	// is resolved to cfg.decimalMode in PersistentPreRunE and honored at
	// normalization time, so it changes what the filter computes on, not just the print.
	root.PersistentFlags().StringVar(&cfg.decimal, "format.decimal", "auto", "how to present a non-integer decimal to the filter: auto (Mongo Decimal128 exact string, Redis fractional number), number (bare, may lose precision), or string (exact, quoted; use tonumber)")
	// Diagnostics flags (sq-compatible). -v is global; `iq ls` reuses it for its
	// driver column. --log* also honor IQ_LOG/IQ_LOG_FILE/IQ_LOG_LEVEL/IQ_LOG_FORMAT.
	root.PersistentFlags().BoolVarP(&cfg.verbose, "verbose", "v", false, "print verbose diagnostics to stderr; for a query, the formatted plan and a live backend command trace (disables the progress spinner)")
	root.PersistentFlags().BoolVar(&cfg.logEnable, "log", false, "enable logging to a file (also via IQ_LOG)")
	root.PersistentFlags().StringVar(&cfg.logFile, "log.file", "", "log file path; empty disables logging (default <user cache dir>/iq/iq.log)")
	root.PersistentFlags().StringVar(&cfg.logLevel, "log.level", "DEBUG", "log level: DEBUG, INFO, WARN, or ERROR")
	root.PersistentFlags().StringVar(&cfg.logFormat, "log.format", "text", "log format: text or json")
	root.PersistentFlags().StringVar(&cfg.errorFormat, "error.format", "text", "error output format: text or json")
	root.PersistentFlags().BoolVar(&cfg.errorStack, "error.stack", false, "print the error cause chain to stderr (may include backend internals)")
	root.PersistentFlags().BoolVar(&cfg.errorTextVerbose, "error.format.text.verbose", true, "for a jq syntax error in text format, show a caret span report")
	root.PersistentFlags().StringVar(&cfg.pprofMode, "debug.pprof", "", "write a runtime profile of the whole run: cpu, mem, block, mutex, goroutine, thread, or trace")
	// --unbounded and --no-compile are local to the default jq action.
	root.Flags().BoolVar(&cfg.unbounded, "unbounded", false, "permit a filter that loads the whole dataset into memory (also materializes a .[]-rooted filter instead of streaming it)")
	root.Flags().BoolVar(&cfg.noCompile, "no-compile", false, "disable server-side predicate pushdown; run the full .[]|select(...) filter client-side (pushdown is on by default for MongoDB, already a no-op on Redis; results are unchanged either way)")
	root.Flags().BoolVar(&cfg.explain, "explain", false, "print the formatted query plan (pretty jq, nested filters, and the backend calls) and exit without connecting or executing")
	root.Flags().StringArrayVar(&cfg.from, "from", nil, "cross-source stage `name=<jq>`: reduce source name with <jq> and bind its results to $name (repeatable; needs --combine)")
	root.Flags().StringVar(&cfg.combine, "combine", "", "final jq over the --from results (each bound to $name), run over a null input")
	// One rendering per run: the format flags are mutually exclusive and default
	// to pretty json when none is set.
	root.Flags().BoolVarP(&cfg.json, "json", "j", false, "output pretty JSON, one value per result (the default rendering)")
	root.Flags().BoolVarP(&cfg.jsonArray, "json-array", "A", false, "output every result wrapped in one [ ... ] JSON document")
	root.Flags().BoolVarP(&cfg.jsonl, "jsonl", "J", false, "output compact JSON, one value per line (JSON Lines)")
	root.Flags().BoolVarP(&cfg.yaml, "yaml", "y", false, "output YAML documents, separated by ---")
	root.Flags().BoolVarP(&cfg.raw, "raw", "r", false, "output scalars unquoted, one per line (objects and arrays fall back to compact JSON)")
	root.Flags().StringVarP(&cfg.format, "format", "f", "", "select the output rendering by name: json (default), jsonl, json-array, yaml, values (alias: raw); an alternative to -j/-J/-A/-y/-r")
	root.MarkFlagsMutuallyExclusive("format", "json", "json-array", "jsonl", "yaml", "raw")
	root.Flags().BoolVar(&cfg.compact, "compact", false, "collapse pretty json / json-array output to single-line (no-op for jsonl, values, yaml)")
	root.AddCommand(
		newExecCmd(cfg),
		newAddCmd(),
		newLsCmd(cfg),
		newRmCmd(),
		newMvCmd(),
		newSrcCmd(),
		newGroupCmd(),
		newConfigCmd(cfg),
		newPingCmd(cfg),
		newInspectCmd(cfg),
		newDiffCmd(cfg),
		newDriverCmd(),
		newVersionCmd(),
	)
	// Render --help flags in labeled sections (Source/Query/Output/Display/
	// Diagnostics) instead of one flat list. Presentation only: parsing and the
	// mutual-exclusivity constraint above are untouched.
	installGroupedHelp(root)
	return root, cfg
}

// validateErrorFormat rejects an --error.format outside {text, json} at PreRun,
// mirroring the fail-fast posture of the -M/-C check.
func validateErrorFormat(f string) error {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "text", "json":
		return nil
	default:
		return fmt.Errorf("invalid --error.format %q: want text or json", f)
	}
}

// openOutputFile opens the --output target for writing, truncating an existing
// file (sq's -o is a fresh write, not an append). A missing parent directory is
// an error; the flag does not create directories. The error is wrapped so it
// anchors at the CLI, not deep in os.
func openOutputFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open output file %q: %w", path, err)
	}
	return f, nil
}

// Execute runs the CLI. It is the composition root: it builds a signal-aware
// context, runs the root command, finalizes the diagnostics resources opened in
// PersistentPreRunE (on both success and error paths), and maps any error to a
// rendered stderr message and a non-zero exit code.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root, cfg := newRootCmd()
	err := root.ExecuteContext(ctx)
	// Record the terminal error before the log file closes. cfg.logger is nil
	// only when a flag-parse error aborted before PersistentPreRunE ran.
	if err != nil && !errors.Is(err, errQuietExit) && cfg.logger != nil {
		cfg.logger.Error("iq failed", "err", err)
	}
	// Finalize in a fixed order: stop/write the profile, then close the log file,
	// then render the error. Each handle is nil when its resource never opened.
	if cfg.pprofStop != nil {
		cfg.pprofStop()
	}
	if cfg.logClose != nil {
		_ = cfg.logClose()
	}
	// Close the --output file after the command has returned; results are already
	// on disk (os.File writes are unbuffered), so this just releases the fd.
	if cfg.outClose != nil {
		_ = cfg.outClose()
	}
	if err != nil {
		// errQuietExit (diff --exit-code) signals a non-zero status with no
		// message: the differences are the report, not an error.
		if !errors.Is(err, errQuietExit) {
			renderError(os.Stderr, cfg, err)
		}
		os.Exit(1)
	}
}

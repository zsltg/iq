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
	// address is the opaque dotted suffix on a source reference (shop.orders ->
	// address "orders"), "" when absent. The core never interprets it; the driver
	// does (MongoDB treats it as a collection). It overrides the source URL's own
	// default (mongo's ?collection=).
	address   string
	timeout   time.Duration
	unbounded bool
	noCompile bool
	explain   bool
	from      []string
	combine   string
	format    string
	json      bool
	jsonArray bool
	jsonl     bool
	yaml      bool
	raw       bool
	gron      bool
	gronArray bool
	compact   bool
	// Write/movement flags (runMove): --insert names a destination source handle;
	// --typed emits {key,type,value} records instead of the jq value stream. The rest
	// mirror the retired `iq data copy`: key derivation, write mode, dry-run, and the
	// import-side --from-format hint (needed for YAML, which is not content-sniffable).
	insert      string
	typed       bool
	moveKey     string
	keyField    string
	keyPrefix   string
	moveType    string
	noOverwrite bool
	replace     bool
	force       bool
	dryRun      bool
	fromFormat  string
	// stdin marks that the source is piped stdin (no --src, stdin not a terminal),
	// buffered once and served through the drivers/file decoders.
	stdin bool
	// decimal is the raw --format.decimal flag; decimalMode is it resolved once in
	// PersistentPreRunE and passed as a plain type into the driver adapters.
	decimal      string
	decimalMode  numfmt.DecimalMode
	monochrome   bool
	forceColor   bool
	noProgress   bool
	noCache      bool
	noCacheIndex bool
	reveal       bool
	expand       bool
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
	// logStructured is true when the structured file/stream sink is active
	// (logOptions.fileActive). It gates the INFO "query plan" record so that a bare
	// --verbose run — whose tinted sink already renders the pretty plan text — does
	// not also emit the one-line structured record; the record is for the structured
	// sink, never the verbose sink alone.
	logStructured bool
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
		Long: "iq runs a jq filter against a NoSQL database.\n" +
			"\n" +
			"  $ iq '.[] | select(.total > 99) | .id' --src shop.orders\n" +
			"\n" +
			"The filter's top-level paths name the keys to fetch — `iq '.greeting'`, or\n" +
			"`iq '.[\"user:1\"]'` for a key with a colon. A `.[]`-rooted filter streams the\n" +
			"whole keyspace in constant memory; a filter that collapses the dataset into one\n" +
			"value (`.`, `keys`, `map(...)`) needs --unbounded. Always single-quote the\n" +
			"filter so the shell leaves its brackets, spaces, and pipes alone.\n" +
			"\n" +
			"A database is a saved source, chosen by URL scheme — redis:// or mongodb://.\n" +
			"Register with `iq add`, pick a default with `iq src`, list with `iq ls`. Address\n" +
			"a MongoDB collection as handle.collection (e.g. --src shop.orders), or set a\n" +
			"default in the source URL (...?collection=orders) and drop the suffix.\n" +
			"\n" +
			"select(...) clauses are pushed to the backend automatically, results unchanged;\n" +
			"run `iq --explain` to see the plan. The README carries the full reference.",
		Example: "  # Register a Redis source (active) and a Mongo source whose password is kept safe.\n" +
			"  # The Mongo URL sets a default collection with ?collection=orders.\n" +
			"  $ iq add -a -n cache redis://localhost:6379/0\n" +
			"  $ iq add -p --store keyring 'mongodb://user@localhost:27017/shop?collection=orders'\n" +
			"\n" +
			"  # List saved sources (the active one marked *); check reachability; inspect metadata.\n" +
			"  $ iq ls\n" +
			"  $ iq ping\n" +
			"  $ iq inspect shop.orders\n" +
			"\n" +
			"  # Fetch one key from the active Redis source.\n" +
			"  $ iq '.greeting'\n" +
			"\n" +
			"  # Query a Mongo collection — select(...) pushes to the backend.\n" +
			"  $ iq '.[] | select(.total > 99) | .id' --src shop.orders\n" +
			"\n" +
			"  # The URL's ?collection= default lets you drop the suffix.\n" +
			"  $ iq '.[]' --src shop\n" +
			"\n" +
			"  # Output as JSON Lines, or one JSON array written to a file.\n" +
			"  $ iq '.[]' --src shop.orders --jsonl\n" +
			"  $ iq '.[]' --src shop.orders -A -o results.json\n" +
			"\n" +
			"  # Join two collections of one source on a shared id (each --from names its source).\n" +
			"  $ iq --from shop.orders='.[] | {id, total}' \\\n" +
			"       --from shop.users='.[] | {id, name}' \\\n" +
			"       --combine '($shop_users | INDEX(.id)) as $u | $shop_orders[] | . + {name: $u[.id].name}'\n" +
			"\n" +
			"  # Query a piped dump file (implicit stdin).\n" +
			"  $ cat dump.jsonl | iq '.[]'",
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
			// The structured "query plan" record is gated on the structured sink, so
			// carry its active-ness onto cfg for the run body to read.
			cfg.logStructured = logOpts.fileActive()
			logger, closeLog, err := logOpts.build(cmd.ErrOrStderr(), cmd.OutOrStdout())
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
				// A combine emits values over a null input, so there are no keys to
				// write with; refuse rather than render and silently drop the write.
				if cfg.insert != "" || cfg.typed {
					return errors.New("cannot use --insert/--typed with --from/--combine; a combine emits values with no keys, so pipe its output into a second `iq --insert` with --key or --key-field")
				}
				return runCombine(cmd, cfg)
			}
			// --insert/--typed turn the command into a data move: the source's items
			// are transformed by the optional positional filter and written to a
			// destination source (--insert) or emitted as a typed dump (--typed). A
			// move needs no filter, so it runs even with no positional.
			if cfg.insert != "" || cfg.typed {
				filter := ""
				if len(args) > 0 {
					filter = args[0]
				}
				return runMove(cmd, cfg, filter)
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
	root.PersistentFlags().DurationVar(&cfg.timeout, "timeout", 5*time.Second, "per-query timeout")
	root.PersistentFlags().BoolVarP(&cfg.monochrome, "monochrome", "M", false, "disable colored output (also honored via NO_COLOR); color is on by default only when writing to a terminal")
	root.PersistentFlags().BoolVarP(&cfg.forceColor, "color", "C", false, "force colored output even when the destination is not a terminal (e.g. a pager)")
	root.PersistentFlags().BoolVar(&cfg.noProgress, "no-progress", false, "disable the scan progress spinner (shown on stderr for long scans when it is a terminal)")
	// --no-cache bypasses the file:// dump decode cache for one run; it is
	// persistable (`iq config set no-cache true`) to turn caching off by default.
	root.PersistentFlags().BoolVar(&cfg.noCache, "no-cache", false, "disable the file:// dump decode cache (on by default for local dump files above 4 MiB; a cached decode skips re-parsing the dump on later queries)")
	// --no-cache-index keeps the flat decode cache but skips its per-page Bloom
	// index — the escape hatch for a very large keyspace where the index build
	// memory is unwelcome; persistable via `iq config set no-cache-index true`.
	root.PersistentFlags().BoolVar(&cfg.noCacheIndex, "no-cache-index", false, "skip the file:// cache's per-page key index (still caches the decode; a bounded key read streams the whole cache instead of decoding only candidate pages)")
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
	root.Flags().BoolVarP(&cfg.jsonArray, "jsona", "A", false, "output every result wrapped in one [ ... ] JSON document (iq's --jsona wraps the whole stream; sq's emits per-row value-arrays)")
	root.Flags().BoolVarP(&cfg.jsonl, "jsonl", "J", false, "output compact JSON, one value per line (JSON Lines)")
	root.Flags().BoolVarP(&cfg.yaml, "yaml", "y", false, "output YAML documents, separated by ---")
	root.Flags().BoolVarP(&cfg.raw, "raw", "r", false, "output scalars unquoted, one per line (objects and arrays fall back to compact JSON)")
	root.Flags().BoolVarP(&cfg.gron, "gron", "g", false, "output flattened assignment statements (gron), one per line: greppable, each result rooted at json, reversible with ungron")
	root.Flags().BoolVarP(&cfg.gronArray, "grona", "G", false, "like --gron but result N roots at json[N], so the whole stream ungrons back to one JSON array (gron's --stream style)")
	root.Flags().StringVarP(&cfg.format, "format", "f", "", "select the output rendering by name: json (default), jsonl, jsona, yaml, values (alias: raw), gron, grona; an alternative to -j/-J/-A/-y/-r/-g/-G")
	root.MarkFlagsMutuallyExclusive("format", "json", "jsona", "jsonl", "yaml", "raw", "gron", "grona")
	root.Flags().BoolVar(&cfg.compact, "compact", false, "collapse pretty json / jsona output to single-line (no-op for jsonl, values, yaml, gron, grona)")
	// Write/movement flags for the default action: --insert redirects results into a
	// destination source (sq's --insert); --typed emits {key,type,value} records — a
	// re-importable dump. The rest mirror the retired `iq data copy`.
	root.Flags().StringVar(&cfg.insert, "insert", "", "write each item into this destination `source` (copy/restore/import) instead of rendering")
	root.Flags().BoolVar(&cfg.typed, "typed", false, "emit typed {key,type,value} records — a re-importable dump (needed for Redis; Mongo's plain output already restores)")
	root.Flags().StringVar(&cfg.moveKey, "key", "", "jq expression yielding each written item's key (--insert/--typed)")
	root.Flags().StringVar(&cfg.keyField, "key-field", "", "object field to take the key from, for foreign JSON input (--insert)")
	root.Flags().StringVar(&cfg.keyPrefix, "key-prefix", "", "string prepended to every written key (--insert/--typed)")
	root.Flags().StringVar(&cfg.moveType, "type", "", "native type stamped on a reshaped value, e.g. hash, list, json (--insert/--typed)")
	root.Flags().BoolVar(&cfg.noOverwrite, "no-overwrite", false, "insert only; skip keys that already exist (--insert)")
	root.Flags().BoolVar(&cfg.replace, "replace", false, "empty the destination before writing, with confirmation (--insert)")
	root.Flags().BoolVar(&cfg.force, "force", false, "skip the confirmation prompt for --replace")
	root.Flags().BoolVar(&cfg.dryRun, "dry-run", false, "report the effect of --insert without writing anything")
	root.Flags().StringVar(&cfg.fromFormat, "from-format", "", "format of a piped-stdin source when it cannot be sniffed: jsonl, json, yaml, mongoexport, bson, rdb, dynamodb-json, cassandra-csv, or neo4j-json")
	root.MarkFlagsMutuallyExclusive("insert", "typed")
	root.AddCommand(
		newExecCmd(cfg),
		newDataCmd(cfg),
		newAddCmd(cfg),
		newLsCmd(cfg),
		newRmCmd(),
		newMvCmd(),
		newSrcCmd(),
		newGroupCmd(),
		newConfigCmd(cfg),
		newCacheCmd(),
		newPingCmd(cfg),
		newInspectCmd(cfg),
		newDiffCmd(cfg),
		newSchemaCmd(cfg),
		newDriverCmd(cfg),
		newVersionCmd(),
		newManCmd(),
	)
	// Render --help flags in labeled sections (Source/Query/Output/Display/
	// Diagnostics) instead of one flat list. Presentation only: parsing and the
	// mutual-exclusivity constraint above are untouched.
	installGroupedHelp(root)
	// Render --help's "Available Commands" in labeled sections (Sources/Query &
	// Data/Configuration/Info) instead of one flat alphabetical list.
	installCommandGroups(root)
	// A bare `iq <jq-filter>` positional is a jq program, never a file, so suppress
	// the shell's default filename completion. --src completes saved source handles.
	root.ValidArgsFunction = cobra.NoFileCompletions
	// RegisterFlagCompletionFunc errors only on an unknown flag; every name below is
	// registered above, so the error cannot fire and swallowing it keeps setup
	// panic-free. --src and --insert both name a saved source; the rest take a
	// closed set of values, each mirroring the validator that rejects the others.
	_ = root.RegisterFlagCompletionFunc("src", completeSourceHandles)
	_ = root.RegisterFlagCompletionFunc("insert", completeSourceHandles)
	_ = root.RegisterFlagCompletionFunc("format", fixedValues(formatNames()...))
	_ = root.RegisterFlagCompletionFunc("from-format", fixedValues(dumpFormatNames...))
	_ = root.RegisterFlagCompletionFunc("format.decimal", fixedValues(decimalModeNames...))
	_ = root.RegisterFlagCompletionFunc("log.level", fixedValues(logLevelNames...))
	_ = root.RegisterFlagCompletionFunc("log.format", fixedValues(textJSONNames...))
	_ = root.RegisterFlagCompletionFunc("error.format", fixedValues(textJSONNames...))
	_ = root.RegisterFlagCompletionFunc("debug.pprof", fixedValues(pprofModes...))
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
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) //nolint:gosec // G302: the --output file uses conventional 0644; the user names the path.
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

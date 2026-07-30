package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
)

// combineStage is one resolved positional spec: the source it names, bound to a
// jq variable for the combine step.
type combineStage struct {
	varName string
	spec    sourceSpec
}

// newCombineCmd builds `iq combine <spec>... --with <jq>`: query several sources
// and join their results in one jq program. Each positional is an ordinary source
// spec — the same `<source>[=<jq>]` grammar `iq diff` and `iq schema` take — and
// binds its results to $name. Bounded by --timeout.
func newCombineCmd(cfg *config) *cobra.Command {
	var with string
	long := "Query several sources and combine their results with one jq program.\n\n" +
		"Each positional is a source spec, `<source>[=<jq>]`, the same grammar `iq diff`\n" +
		"and `iq schema` take. The spec's results bind to a jq variable named after the\n" +
		"source, with '/', '.' and '-' becoming '_' — so `prod/books=.[]` binds $prod_books\n" +
		"— and --with is the final program, run over a null input with every variable in\n" +
		"scope.\n\n" +
		"Each source reduces at the source, and a pushable filter pushes down, so this\n" +
		"never copies whole datasets to join them. A spec with no filter binds the whole\n" +
		"keyspace, which is a holistic read: it needs --unbounded, exactly as the same\n" +
		"expression would on a plain query.\n\n" +
		"A combine emits values over a null input, so its results carry no keys. To write\n" +
		"them somewhere, pipe the output into a second `iq --insert` with --key or\n" +
		"--key-field."
	c := &cobra.Command{
		Use:               "combine <source>[=<jq>]... --with <jq>",
		ValidArgsFunction: completeSourceHandles,
		Short:             "Query several sources and combine their results",
		Long:              long,
		Example: "  # Join two sources: every order whose customer is in the other source.\n" +
			"  $ iq combine 'orders=.[]' 'customers=.[] | .id' \\\n" +
			"      --with '$orders | map(select(.customer as $c | $customers | index($c)))'\n" +
			"\n" +
			"  # Reduce each side first, then compare the totals.\n" +
			"  $ iq combine 'prod=[.[] | .total] | add' 'staging=[.[] | .total] | add' \\\n" +
			"      --with '{prod: $prod[0], staging: $staging[0]}'\n" +
			"\n" +
			"  # A spec with no filter binds the whole keyspace (holistic, so --unbounded).\n" +
			"  $ iq combine prod staging --with '$prod + $staging' --unbounded",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCombine(cmd, cfg, args, with)
		},
	}
	c.Flags().StringVar(&with, "with", "", "final jq over the bound source results (each spec's results bound to $name), run over a null input")
	c.Flags().BoolVar(&cfg.unbounded, "unbounded", false, "permit a spec that loads a whole keyspace into memory (also materializes a .[]-rooted filter instead of streaming it)")
	c.Flags().BoolVar(&cfg.noCompile, "no-compile", false, "disable server-side predicate pushdown; run each spec's full .[]|select(...) filter client-side (results are unchanged either way)")
	c.Flags().BoolVar(&cfg.explain, "explain", false, "print the formatted query plan (pretty jq, per-source filters, and the backend calls) and exit without connecting or executing")
	addRenderFlags(c, cfg)
	return c
}

// runCombine runs each spec's jq against its own source (reduced and, where the
// backend allows, pushed down), binds the result set to $name, then runs the
// --with program over the bound variables. Each source reduces at the source, so
// this never copies whole datasets.
func runCombine(cmd *cobra.Command, cfg *config, args []string, with string) error {
	if strings.TrimSpace(with) == "" {
		return errors.New("combine needs --with to say how to combine the results")
	}
	// Resolve the output format before any source work, so conflicting format
	// flags fail fast rather than after opening and scanning every source.
	fm, err := selectFormat(cfg)
	if err != nil {
		return err
	}
	if err := guardBinaryFormat(fm, cmd.OutOrStdout()); err != nil {
		return err
	}
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	stages, err := planCombine(cf, args)
	if err != nil {
		return err
	}

	// --explain prints the plan to stdout and stops before any connection; --verbose
	// prints it to stderr and turns on the live command trace for the run below.
	if cfg.explain || cfg.verbose {
		plan, err := buildCombinePlan(cfg, stages, with, cfg.verbose)
		if err != nil {
			return err
		}
		if cfg.explain {
			_, _ = fmt.Fprint(cmd.OutOrStdout(), plan)
			return nil
		}
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), plan)
		cfg.trace = cmd.ErrOrStderr()
	}

	// When logging is active, tee each stage's driver command trace into the log,
	// so a --log combine run captures the wire trace without --verbose.
	cfg.trace = traceSink(cfg.trace, cfg.log())

	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
	defer cancel()

	cfg.log().Info("combine start", "stages", len(stages))
	opts := query.RunOptions{Unbounded: cfg.unbounded, Compile: !cfg.noCompile, Logger: cfg.log()}
	names := make([]string, 0, len(stages))
	values := make([]any, 0, len(stages))
	for _, st := range stages {
		cfg.log().Debug("from source", "handle", st.spec.handle)
		cfg.logStagePlan(st.spec.url, st.spec.handle, st.spec.filter)
		// The stage config carries the logger so its store decorator and scan
		// records fire too; the trace writer is already the log-teed sink.
		vals, err := collectSource(ctx, &config{url: st.spec.url, address: st.spec.address, trace: cfg.trace, decimalMode: cfg.decimalMode, logger: cfg.logger}, st.spec.filter, opts)
		if err != nil {
			if errors.Is(err, query.ErrScanNotAllowed) {
				return fmt.Errorf("source %q: %w; add --unbounded or give the spec a .[]-rooted filter", st.spec.handle, err)
			}
			return fmt.Errorf("source %q: %w", st.spec.handle, err)
		}
		names = append(names, "$"+st.varName)
		values = append(values, vals)
	}

	f := newFormatter(fm, cmd.OutOrStdout(), cfg.compact)
	runErr := query.NewCombiner().Run(ctx, with, names, values, f.emit)
	cfg.log().Info("combine complete", "stages", len(stages))
	return finish(f, asSyntaxError(with, runErr))
}

// planCombine parses and resolves the positional specs into stages, rejecting a
// malformed spec, an unknown source, or two specs that would bind the same
// variable. A spec with no filter binds the whole keyspace: "." is what the
// grammar's omitted filter means everywhere, and the engine's own scan guard
// decides whether that holistic read is allowed.
func planCombine(cf *iqconfig.Config, specs []string) ([]combineStage, error) {
	stages := make([]combineStage, 0, len(specs))
	bound := make(map[string]string, len(specs))
	for _, spec := range specs {
		s, err := parseSourceSpec(cf, spec, ".")
		if err != nil {
			return nil, fmt.Errorf("invalid source %q: %w", spec, err)
		}
		name, _, _ := strings.Cut(spec, "=")
		v := varName(name)
		if prev, dup := bound[v]; dup {
			return nil, fmt.Errorf("%q and %q both bind $%s; rename one", prev, name, v)
		}
		bound[v] = name
		stages = append(stages, combineStage{varName: v, spec: s})
	}
	return stages, nil
}

// logStagePlan emits the structured "query plan" record for one combine stage at
// INFO when the structured file/stream sink is active, so each cross-source
// stage's classification, planned ops, and pushdown decisions are captured
// (distinguished by the handle attr). Like logQueryPlan it is gated on the
// structured sink, not the verbose sink, so a bare -v does not render the plan
// twice. It shares buildSourcePlan with the --explain text, so the two never
// drift; building the plan is skipped when the structured sink is off or below INFO.
func (cfg *config) logStagePlan(url, handle, filter string) {
	if !cfg.logStructured {
		return
	}
	lg := cfg.log()
	if !lg.Enabled(context.Background(), slog.LevelInfo) {
		return
	}
	sp, err := buildSourcePlan(url, filter, !cfg.noCompile, cfg.unbounded)
	if err != nil || !sp.hasPlan {
		return
	}
	lg.Info("query plan", sp.logAttrs(handle)...)
}

// collectSource opens the source, runs filter through the engine, and gathers
// its (reduced) output into a slice to bind for the combine step.
func collectSource(ctx context.Context, cfg *config, filter string, opts query.RunOptions) ([]any, error) {
	st, err := openStore(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = st.Close() }()
	out := []any{}
	err = query.NewJQEngine(st).Run(ctx, filter, opts, func(v any) error {
		out = append(out, v)
		return nil
	})
	return out, err
}

// varName derives the jq variable a combine spec binds to: its name with group
// separators and dots or dashes replaced by "_", and a leading digit escaped, so
// "orders" binds $orders and "prod/books" binds $prod_books.
func varName(name string) string {
	v := strings.NewReplacer("/", "_", ".", "_", "-", "_").Replace(name)
	if v != "" && v[0] >= '0' && v[0] <= '9' {
		v = "_" + v
	}
	return v
}

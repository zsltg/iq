package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
)

// fromStage is one resolved --from clause: a source and the jq that reduces it,
// bound to a variable for the combine step. address is the dotted override on the
// source spec (shop.orders), "" when absent.
type fromStage struct {
	varName string
	handle  string
	source  iqconfig.Source
	address string
	filter  string
}

// runCombine is the cross-source action (--from/--combine): it runs each --from
// clause's jq against its source (reduced and, on MongoDB, pushed down), binds
// the result set to $name, then runs --combine over the bound variables. Each
// source reduces at the source, so this never copies whole datasets.
func runCombine(cmd *cobra.Command, cfg *config) error {
	if len(cfg.from) == 0 {
		return errors.New("--combine requires at least one --from source")
	}
	if strings.TrimSpace(cfg.combine) == "" {
		return errors.New("--from requires --combine to say how to combine the results")
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
	stages, err := planFrom(cf, cfg.from)
	if err != nil {
		return err
	}

	// --explain prints the plan to stdout and stops before any connection; --verbose
	// prints it to stderr and turns on the live command trace for the run below.
	if cfg.explain || cfg.verbose {
		plan, err := buildCombinePlan(cfg, stages)
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

	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
	defer cancel()

	cfg.log().Info("combine start", "stages", len(stages))
	opts := query.RunOptions{Unbounded: cfg.unbounded, Compile: !cfg.noCompile}
	names := make([]string, 0, len(stages))
	values := make([]any, 0, len(stages))
	for _, st := range stages {
		cfg.log().Debug("from source", "handle", st.handle)
		u, err := effectiveURL(st.source, st.handle)
		if err != nil {
			return fmt.Errorf("--from %q: %w", st.handle, err)
		}
		vals, err := collectSource(ctx, &config{url: u, address: st.address, trace: cfg.trace, decimalMode: cfg.decimalMode}, st.filter, opts)
		if err != nil {
			if errors.Is(err, query.ErrScanNotAllowed) {
				return fmt.Errorf("--from %q: %w; add --unbounded or use a .[]-rooted filter", st.handle, err)
			}
			return fmt.Errorf("--from %q: %w", st.handle, err)
		}
		names = append(names, "$"+st.varName)
		values = append(values, vals)
	}

	f := newFormatter(fm, cmd.OutOrStdout(), cfg.compact)
	runErr := query.NewCombiner().Run(ctx, cfg.combine, names, values, f.emit)
	cfg.log().Info("combine complete", "stages", len(stages))
	return finish(f, asSyntaxError(cfg.combine, runErr))
}

// planFrom parses and resolves the --from clauses into stages, rejecting a
// malformed clause, an unknown source, or two clauses that would bind the same
// variable.
func planFrom(cf *iqconfig.Config, specs []string) ([]fromStage, error) {
	stages := make([]fromStage, 0, len(specs))
	bound := make(map[string]string, len(specs))
	for _, spec := range specs {
		name, filter, ok := strings.Cut(spec, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid --from %q: expected name=<jq filter>", spec)
		}
		if strings.TrimSpace(filter) == "" {
			return nil, fmt.Errorf("invalid --from %q: empty filter", spec)
		}
		base, addr, _ := splitSourceArg(cf, name)
		src, handle, found := cf.Resolve(base)
		if !found {
			return nil, fmt.Errorf("unknown source %q in --from; run `iq ls`", base)
		}
		if err := addressUnsupported(src.URL, addr); err != nil {
			return nil, fmt.Errorf("--from %q: %w", name, err)
		}
		v := varName(name)
		if prev, dup := bound[v]; dup {
			return nil, fmt.Errorf("--from %q and %q both bind $%s; rename one", prev, name, v)
		}
		bound[v] = name
		stages = append(stages, fromStage{varName: v, handle: handle, source: src, address: addr, filter: filter})
	}
	return stages, nil
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

// varName derives the jq variable a --from source binds to: its name with group
// separators and dots or dashes replaced by "_", and a leading digit escaped, so
// "orders" binds $orders and "prod/books" binds $prod_books.
func varName(name string) string {
	v := strings.NewReplacer("/", "_", ".", "_", "-", "_").Replace(name)
	if v != "" && v[0] >= '0' && v[0] <= '9' {
		v = "_" + v
	}
	return v
}

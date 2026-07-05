package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
)

// lifecycleOp captures the difference between the two lifecycle verbs so clear and
// drop share one runner. supported reports whether an opened store has the
// capability (Clearer / Dropper); run performs it; describe returns the static
// --explain plan and whether the backend supports the op at all.
type lifecycleOp struct {
	name      string
	supported func(st store) bool
	run       func(ctx context.Context, st store) error
	describe  func(d driver) (query.AccessPlan, bool)
}

var clearOp = lifecycleOp{
	name:      "clear",
	supported: func(st store) bool { _, ok := st.(query.Clearer); return ok },
	run:       func(ctx context.Context, st store) error { return st.(query.Clearer).Clear(ctx) },
	describe: func(d driver) (query.AccessPlan, bool) {
		if d.explainClear == nil {
			return query.AccessPlan{}, false
		}
		return d.explainClear(), true
	},
}

var dropOp = lifecycleOp{
	name:      "drop",
	supported: func(st store) bool { _, ok := st.(query.Dropper); return ok },
	run:       func(ctx context.Context, st store) error { return st.(query.Dropper).Drop(ctx) },
	describe: func(d driver) (query.AccessPlan, bool) {
		if d.explainDrop == nil {
			return query.AccessPlan{}, false
		}
		return d.explainDrop()
	},
}

// newDataClearCmd builds `iq data clear <target>...`: empty one or more containers,
// keeping them. It works on every backend, since emptying is universal.
func newDataClearCmd(cfg *config, df *dataFlags) *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "clear <target>...",
		Short: "Empty one or more containers (keep them)",
		Long: "Empty a collection/keyspace, keeping the container. MongoDB: deleteMany({}).\n" +
			"Redis: FLUSHDB. Distinct from `iq rm`, which only unregisters a saved source —\n" +
			"clear destroys the stored data.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLifecycle(cmd, cfg, df, force, args, clearOp)
		},
	}
	c.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return c
}

// newDataDropCmd builds `iq data drop <target>...`: remove one or more containers.
// It is offered only where a backend can remove its container — MongoDB drops a
// collection; Redis, whose DB index is not removable, is rejected via the absent
// Dropper capability.
func newDataDropCmd(cfg *config, df *dataFlags) *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "drop <target>...",
		Short: "Remove one or more containers",
		Long: "Remove a container entirely. MongoDB: drop the collection and its indexes.\n" +
			"Redis has no droppable container (a DB index only empties), so drop is rejected\n" +
			"for a Redis target — use `iq data clear`. Distinct from `iq rm`, which only\n" +
			"unregisters a saved source; drop destroys the stored data.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLifecycle(cmd, cfg, df, force, args, dropOp)
		},
	}
	c.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return c
}

// runLifecycle applies op to each target: printing the plan (--explain), reporting
// the intended effect (--dry-run), or performing it after confirmation. A target
// must be a source (not a file), and the backend must support the op.
func runLifecycle(cmd *cobra.Command, cfg *config, df *dataFlags, force bool, args []string, op lifecycleOp) error {
	if err := df.validate(); err != nil {
		return err
	}
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	targets := make([]endpoint, len(args))
	for i, arg := range args {
		ep, err := resolveEndpoint(cf, arg, false)
		if err != nil {
			return err
		}
		if ep.isFile {
			return fmt.Errorf("%s needs a source, not a file: %q", op.name, arg)
		}
		targets[i] = ep
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
	defer cancel()

	for _, t := range targets {
		if df.explain {
			printLifecyclePlan(cmd, op, t)
			continue
		}
		if err := applyLifecycle(cmd, ctx, op, t, df.dryRun, force); err != nil {
			return err
		}
	}
	return nil
}

// applyLifecycle opens the target, checks the capability, then reports (--dry-run)
// or performs the op after confirmation.
func applyLifecycle(cmd *cobra.Command, ctx context.Context, op lifecycleOp, t endpoint, dry, force bool) error {
	st, err := openStore(ctx, &config{url: t.url, collection: t.collection})
	if err != nil {
		return redactErr(err, t.url)
	}
	defer func() { _ = st.Close() }()

	if !op.supported(st) {
		return fmt.Errorf("%s: backend %s does not support %s", t.label(), t.driver, op.name)
	}
	if dry {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "would %s %s%s\n", op.name, t.label(), affectedSuffix(ctx, st))
		return nil
	}
	if err := confirmDestruction(cmd, fmt.Sprintf("%s %s", op.name, t.label()), force); err != nil {
		return err
	}
	if err := op.run(ctx, st); err != nil {
		return redactErr(err, t.url)
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%sed %s\n", op.name, t.label())
	return nil
}

// affectedSuffix appends an approximate item count to a dry-run line when the store
// can estimate one cheaply, so a dry run hints at the blast radius without a scan.
func affectedSuffix(ctx context.Context, st store) string {
	est, ok := st.(query.Estimator)
	if !ok {
		return ""
	}
	n, err := est.EstimateCount(ctx)
	if err != nil {
		return ""
	}
	return fmt.Sprintf(" (~%d item(s))", n)
}

// printLifecyclePlan renders the static --explain output for one target: the op,
// the target, and the driver's describer lines (which state "unsupported" for a
// backend that cannot perform the op).
func printLifecyclePlan(cmd *cobra.Command, op lifecycleOp, t endpoint) {
	var b strings.Builder
	b.WriteString(planHeader(op.name+" plan") + "\n")
	writePlanLine(&b, "target", t.label())
	writePlanSection(&b, t.driver+" "+op.name)
	if d, ok := driverForScheme(schemeOf(t.url)); ok {
		plan, _ := op.describe(d)
		for _, line := range plan.Ops {
			b.WriteString("  " + line + "\n")
		}
	}
	_, _ = fmt.Fprint(cmd.OutOrStdout(), b.String())
}

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

// deleteOp reuses the lifecycleOp shape only for the parts the delete command
// shares with clear/drop: the capability probe (Deleter) and the --explain
// describer. Its run field stays nil deliberately — delete carries a key list and
// returns a DeleteStat, so applyDataDelete calls Deleter.Delete directly rather
// than through the operand-less lifecycleOp.run.
var deleteOp = lifecycleOp{
	name:      "delete",
	supported: func(st store) bool { _, ok := st.(query.Deleter); return ok },
	describe: func(d driver) (query.AccessPlan, bool) {
		if d.explainDelete == nil {
			return query.AccessPlan{}, false
		}
		return d.explainDelete()
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
		Example: "  $ iq data clear cache             # empty a Redis source (FLUSHDB)\n" +
			"  $ iq data clear shop.orders shop.users # empty two Mongo collections",
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
		Example: "  $ iq data drop shop.orders             # drop one Mongo collection\n" +
			"  $ iq data drop shop.orders shop.users  # drop several",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLifecycle(cmd, cfg, df, force, args, dropOp)
		},
	}
	c.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return c
}

// newDataDeleteCmd builds `iq data delete <target> <key>...`: remove a named set of
// keys from a container, keeping the container. Offered only where a backend removes
// a record by its canonical key (the Deleter capability); a backend without one (a
// dump file, an InfluxDB point with no row identity) is rejected. Unlike clear/drop,
// it does not prompt: the explicit key list the user typed is its own confirmation.
func newDataDeleteCmd(cfg *config, df *dataFlags) *cobra.Command {
	c := &cobra.Command{
		Use:   "delete <target> <key>...",
		Short: "Remove specific keys from a container (keep the container)",
		Long: "Remove one or more keys from a container, keeping the container. Each key uses\n" +
			"the same spelling as a Get: a bare string (`book:1`), or a JSON array for a\n" +
			"composite key (`[\"shop\",42]`). A key that is already absent is not an error —\n" +
			"delete is idempotent, so a re-run converges. Distinct from `iq data clear`, which\n" +
			"empties the whole container, and from `iq rm`, which only unregisters a source.\n" +
			"No confirmation prompt: the key list you typed is the confirmation; use --dry-run\n" +
			"to preview. Rejected for a backend with no per-key delete (e.g. a dump file).",
		Example: "  $ iq data delete cache book:1 book:2   # remove two Redis keys\n" +
			"  $ iq data delete shop.orders '[\"eu\",42]' # a composite-key row\n" +
			"  $ iq data delete cache book:1 --dry-run   # preview without deleting",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDataDelete(cmd, cfg, df, args[0], args[1:])
		},
	}
	return c
}

// runDataDelete resolves the single target, validates the flags and key list, then
// either prints the plan (--explain) or applies the delete. A target must be a
// source (not a file), and the backend must implement Deleter.
func runDataDelete(cmd *cobra.Command, cfg *config, df *dataFlags, arg string, rawKeys []string) error {
	if err := df.validate(); err != nil {
		return err
	}
	keys, err := dedupeKeys(rawKeys)
	if err != nil {
		return err
	}
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	t, err := resolveEndpoint(cf, arg, false)
	if err != nil {
		return err
	}
	if t.isFile {
		return fmt.Errorf("delete needs a source, not a file: %q", arg)
	}
	if df.explain {
		printLifecyclePlan(cmd, deleteOp, t)
		return nil
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
	defer cancel()
	return applyDataDelete(cmd, ctx, t, keys, df.dryRun)
}

// dedupeKeys drops repeated keys, preserving first-seen order so each key is deleted
// once and the DeleteStat's Deleted+Missing==len(keys) invariant holds. An empty key
// is rejected at entry — no backend has an empty canonical key, so it is bad input.
func dedupeKeys(raw []string) ([]string, error) {
	seen := make(map[string]struct{}, len(raw))
	keys := make([]string, 0, len(raw))
	for _, k := range raw {
		if k == "" {
			return nil, errors.New("delete: an empty key is not allowed")
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
	}
	return keys, nil
}

// applyDataDelete opens the target, checks the Deleter capability, then reports
// (--dry-run) or performs the delete, printing an honest DeleteStat.
func applyDataDelete(cmd *cobra.Command, ctx context.Context, t endpoint, keys []string, dry bool) error {
	st, err := openStore(ctx, &config{url: t.url, address: t.address})
	if err != nil {
		return redactErr(err, t.url)
	}
	defer func() { _ = st.Close() }()

	if !deleteOp.supported(st) {
		return fmt.Errorf("%s: backend %s does not support delete", t.label(), t.driver)
	}
	if dry {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "would delete %d key(s) from %s\n", len(keys), t.label())
		return nil
	}
	stat, err := st.(query.Deleter).Delete(ctx, keys)
	if err != nil {
		return redactErr(err, t.url)
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "deleted %d key(s), %d already absent\n", stat.Deleted, stat.Missing)
	return nil
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
	st, err := openStore(ctx, &config{url: t.url, address: t.address})
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

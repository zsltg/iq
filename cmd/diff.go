package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/diff"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/shape"
)

// errQuietExit signals a non-zero exit with no stderr line: `diff` returns it
// whenever differences exist (diff(1)-style), so scripts can branch on the exit
// status without the report being mistaken for an error. Execute recognizes it.
var errQuietExit = errors.New("differences found")

// errStopSampling stops a ScanBatches walk once the schema sample is full. It is
// a control signal, never surfaced.
var errStopSampling = errors.New("sample complete")

// newDiffCmd builds `iq diff <a> <b>`: compare two saved sources. --data (the
// default) diffs items key by key; --stats diffs native introspection; --schema
// diffs an inferred field/type shape. Layers combine. Bounded by --timeout.
func newDiffCmd(cfg *config) *cobra.Command {
	var (
		dataMode, statsMode, schemaMode bool
		jsonOut, yamlOut, patchOut      bool
		setArrays                       bool
		sections                        []string
		sample                          int
		filter                          string
	)
	long := "Compare two saved sources across the layers a schemaless store can meaningfully\n" +
		"compare. Selecting no layer defaults to --data; layers combine.\n\n" +
		"  --data    item-level diff: added / removed / changed, keyed by document _id\n" +
		"            (MongoDB) or key (Redis). Unfiltered it reads both keyspaces fully\n" +
		"            into memory, so it costs memory proportional to the two sources, a\n" +
		"            deliberate tradeoff, since an added/removed diff needs both key sets\n" +
		"            at once; a filter narrows that read, and a pushable one narrows it\n" +
		"            at the backend.\n" +
		"            Allowed across drivers (Mongo _id vs Redis key): useful for\n" +
		"            verifying a migration, but the identity match is only as meaningful\n" +
		"            as the keys lining up, a power-user tool, not a schema comparison.\n" +
		"  --stats   diff native introspection trees (MongoDB diagnostic commands, Redis\n" +
		"            INFO). Same driver only. --section narrows which sections, comma-\n" +
		"            separated or repeated (`--section memory,server`).\n" +
		"  --schema  diff an inferred field->type shape sampled from each source. Allowed\n" +
		"            across drivers: the inferred vocabulary (integer/number/string(fmt)/\n" +
		"            map/array + required/optional) measures logical shape, not per-driver\n" +
		"            introspection. The shape is sampled (--sample) and inferred, never\n" +
		"            declared, so a wider sample yields a truer shape.\n\n" +
		"Use -j/--json or -y/--yaml for a machine-readable delta. diff exits non-zero when\n" +
		"the sources differ and zero when they match (diff(1)-style), so scripts can branch\n" +
		"on the exit status.\n\n" +
		"Each side may carry a jq filter, so a diff can be scoped to part of a keyspace:\n" +
		"per side as `<source>=<jq>`, or for both at once with --filter, a side's own\n" +
		"filter winning. Write the filter `.[]`-rooted, as on a plain query (iteration\n" +
		"is not implicit here, unlike --insert), because a keyed diff needs each item to\n" +
		"keep its key:\n\n" +
		"    iq diff prod staging --filter '.[] | select(.status == \"new\")'\n\n" +
		"A filter may instead name a single key (prod=.[\"orders:42\"]) to compare one\n" +
		"document. One that collapses the keyspace, or fans an item out into several\n" +
		"values, is refused: neither leaves a key to match on. --stats takes no filter,\n" +
		"it diffs the backend's own introspection, which has no items.\n\n" +
		"--set-arrays compares every array order-insensitively as a multiset (duplicates\n" +
		"counted), reporting membership deltas at the array's own path. --patch emits an\n" +
		"RFC 6902 JSON Patch instead of the human/JSON delta; it renders exactly one layer\n" +
		"(choose one of --data/--stats/--schema) and excludes --json, --yaml, and\n" +
		"--set-arrays."
	c := &cobra.Command{
		Use:               "diff <a>[=<jq>] <b>[=<jq>]",
		ValidArgsFunction: completeSourceHandles,
		Short:             "Compare two sources by data, stats, or inferred schema",
		Long:              long,
		Example: "  # Item-level diff (the default layer) of two collections in one source.\n" +
			"  $ iq diff shop.orders shop.users\n" +
			"\n" +
			"  # Scope both sides to part of the keyspace, or each side separately.\n" +
			"  $ iq diff prod staging --filter '.[] | select(.status == \"new\")'\n" +
			"  $ iq diff 'prod=.[] | select(.type == \"order\")' 'staging=.[] | select(.kind == \"ORDER\")'\n" +
			"  $ iq diff 'prod=.[\"orders:42\"]' 'staging=.[\"orders:42\"]'   # one document\n" +
			"\n" +
			"  # Across environments; compare shapes or native stats.\n" +
			"  $ iq diff prod/shop staging/shop --schema\n" +
			"  $ iq diff prod/shop staging/shop --stats\n" +
			"  $ iq diff prod/shop staging/shop --stats --section memory,server\n" +
			"  $ iq diff prod/shop staging/shop -j",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			left, err := parseSourceSpec(cf, args[0], filter)
			if err != nil {
				return err
			}
			right, err := parseSourceSpec(cf, args[1], filter)
			if err != nil {
				return err
			}
			run := diffRun{
				cfg:      cfg,
				left:     left,
				right:    right,
				sections: sections,
				sample:   sample,
				opts:     diff.Options{SetArrays: setArrays},
			}
			modes := diffModes{
				data:   dataMode,
				stats:  statsMode,
				schema: schemaMode,
				json:   jsonOut,
				yaml:   yamlOut,
				patch:  patchOut,
			}
			return runDiff(cmd, run, modes)
		},
	}
	c.Flags().BoolVar(&dataMode, "data", false, "diff items key by key (default when no layer is chosen; cross-driver allowed)")
	c.Flags().BoolVar(&statsMode, "stats", false, "diff native introspection trees (same driver only)")
	c.Flags().BoolVar(&schemaMode, "schema", false, "diff an inferred field/type shape (cross-driver allowed)")
	c.Flags().StringSliceVar(&sections, "section", nil, "introspection sections or subcommands for --stats (comma-separated or repeatable; iq inspect --list names them; default: the source's full set)")
	// --section names the same introspection subcommands as `inspect --only`, so
	// it shares that completion and its comma-separated list shape; the driver
	// comes offline from the first target.
	_ = c.RegisterFlagCompletionFunc("section", completeInspectOnly)
	c.Flags().IntVar(&sample, "sample", 1000, "max items sampled per side for --schema (0 = all)")
	c.Flags().StringVar(&filter, "filter", "", "jq filter rooted at .[] scoping both sides; a spec's own source=<jq> overrides it for that side")
	c.Flags().BoolVar(&setArrays, "set-arrays", false, "compare arrays order-insensitively as multisets (duplicates counted)")
	c.Flags().BoolVarP(&jsonOut, "json", "j", false, "emit machine-readable JSON")
	c.Flags().BoolVarP(&yamlOut, "yaml", "y", false, "emit machine-readable YAML")
	c.Flags().BoolVar(&patchOut, "patch", false, "emit an RFC 6902 JSON Patch (single layer only; excludes --json/--yaml/--set-arrays)")
	c.MarkFlagsMutuallyExclusive("json", "yaml", "patch")
	c.MarkFlagsMutuallyExclusive("patch", "set-arrays")
	return c
}

// diffRun holds the inputs that the diff layers share: the config, the two
// sides, the --section names, the --sample size, and the diff options. The CLI
// and the MCP tool both build it.
type diffRun struct {
	cfg      *config
	left     sourceSpec
	right    sourceSpec
	sections []string
	sample   int
	opts     diff.Options
}

// diffModes holds the layer and output flags of the diff command.
type diffModes struct {
	data, stats, schema bool
	json, yaml, patch   bool
}

// validate checks the flags of the diff command against the two sides, in
// order. --patch needs one layer. No layer selects --data. --stats refuses a
// filter on either side.
func (m *diffModes) validate(left, right sourceSpec) error {
	if m.patch && layerCount(m.data, m.stats, m.schema) > 1 {
		return errors.New("--patch renders one JSON Patch, so it needs a single layer; choose exactly one of --data, --stats, --schema")
	}
	if !m.data && !m.stats && !m.schema {
		m.data = true
	}
	// Guarded here rather than in the stats collector so both the human and
	// the --patch paths are covered: --patch reaches statsTrees directly.
	if m.stats && (left.filter != "" || right.filter != "") {
		return errors.New("--stats diffs the backend's own introspection, which has no items to filter; drop the filter or diff --data/--schema")
	}
	return nil
}

// runDiff validates the modes, then runs the selected layers under one shared
// --timeout and renders them. It returns errQuietExit when the sources differ.
func runDiff(cmd *cobra.Command, run diffRun, modes diffModes) error {
	if err := modes.validate(run.left, run.right); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), run.cfg.timeout)
	defer cancel()

	if modes.patch {
		// The one chosen layer, rendered as an RFC 6902 patch. --data reads
		// both keyspaces fully, so it carries the same spinner as the
		// human path; stop it before the error check so the line clears.
		meter := newProgressMeter(cmd.ErrOrStderr(), run.cfg.noProgress)
		raw, err := run.patchLayer(ctx, modes.stats, modes.schema, meter.Tick)
		meter.Stop()
		if err != nil {
			return err
		}
		return renderPatch(cmd.OutOrStdout(), raw)
	}

	rep := report{dataRun: modes.data, statsRun: modes.stats, schemaRun: modes.schema}
	if modes.data {
		// --data reads both keyspaces fully with no output until the
		// report renders, so a spinner fits cleanly. Stop it before the
		// error check and before render, so the line clears either way.
		meter := newProgressMeter(cmd.ErrOrStderr(), run.cfg.noProgress)
		d, err := run.diffData(ctx, meter.Tick)
		meter.Stop()
		if err != nil {
			return err
		}
		rep.Data = d
	}
	if modes.stats {
		s, err := run.diffStats(ctx)
		if err != nil {
			return err
		}
		rep.Stats = s
	}
	if modes.schema {
		s, err := run.diffSchema(ctx)
		if err != nil {
			return err
		}
		rep.Schema = s
	}

	if err := rep.render(cmd.OutOrStdout(), run.left, run.right, modes.json, modes.yaml); err != nil {
		return err
	}
	if !rep.empty() {
		return errQuietExit
	}
	return nil
}

// diffData reads both sides and diffs them key by key under opts. Each side is
// its whole keyspace unless its spec carries a filter.
func (r diffRun) diffData(ctx context.Context, onPage func(int)) ([]diff.ItemDelta, error) {
	a, b, err := r.readBoth(ctx, onPage)
	if err != nil {
		return nil, err
	}
	return diff.KeyedOpt(a, b, r.opts), nil
}

// readBoth materializes both sides, ticking onPage per page. It is the shared
// collector behind the keyed data diff and the --patch data layer.
func (r diffRun) readBoth(ctx context.Context, onPage func(int)) (a, b map[string]any, err error) {
	a, err = readAll(ctx, r.cfg, r.left, onPage)
	if err != nil {
		return nil, nil, err
	}
	b, err = readAll(ctx, r.cfg, r.right, onPage)
	if err != nil {
		return nil, nil, err
	}
	return a, b, nil
}

// readAll materializes one side as key -> value: the whole keyspace, or the
// items a spec filter keeps. A key repeated across scan pages (the store's weak
// scan guarantee) simply overwrites.
func readAll(ctx context.Context, cfg *config, t sourceSpec, onPage func(int)) (map[string]any, error) {
	st, err := openStore(ctx, t.runConfig(cfg))
	if err != nil {
		return nil, redactErr(err, t.url)
	}
	defer func() { _ = st.Close() }()
	all := map[string]any{}
	if t.filter != "" {
		// A filtered read keeps each surviving item under its own key, so the diff
		// still matches by key; a pushable predicate narrows the scan at the store,
		// making a scoped diff read less rather than merely report less.
		opts := cfg.runOptions()
		opts.OnPage = onPage
		err = query.NewJQEngine(st).RunKeyed(ctx, t.filter, opts, func(k string, v any) error {
			all[k] = v
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", t.handle, redactErr(asSyntaxError(t.filter, err), t.url))
		}
		return all, nil
	}
	err = st.ScanBatches(ctx, func(batch map[string]any) error {
		if onPage != nil {
			onPage(len(batch))
		}
		maps.Copy(all, batch)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", t.handle, redactErr(err, t.url))
	}
	return all, nil
}

// diffStats runs the same native introspection against both sources and diffs the
// replies under opts. Both sources must use the same driver.
func (r diffRun) diffStats(ctx context.Context) ([]diff.Change, error) {
	a, b, err := r.statsTrees(ctx)
	if err != nil {
		return nil, err
	}
	return diff.TreeOpt(a, b, r.opts), nil
}

// statsTrees collects both sources' native introspection trees, rejecting a
// cross-driver pair first. It is the shared collector behind the stats diff and
// the --patch stats layer.
func (r diffRun) statsTrees(ctx context.Context) (a, b map[string]any, err error) {
	if r.left.driver != r.right.driver {
		return nil, nil, fmt.Errorf("stats diff needs two sources of the same driver; %q is %s and %q is %s", r.left.handle, r.left.driver, r.right.handle, r.right.driver)
	}
	a, err = collectInspect(ctx, r.left, r.sections)
	if err != nil {
		return nil, nil, err
	}
	b, err = collectInspect(ctx, r.right, r.sections)
	if err != nil {
		return nil, nil, err
	}
	return a, b, nil
}

// collectInspect runs the introspection commands for a source and returns the
// replies as one tree: Mongo keys each diagnostic reply by subcommand; Redis
// parses INFO into section -> key -> value.
func collectInspect(ctx context.Context, t sourceSpec, sections []string) (map[string]any, error) {
	st, err := openStore(ctx, &config{url: t.url, address: t.address})
	if err != nil {
		return nil, redactErr(err, t.url)
	}
	defer func() { _ = st.Close() }()

	if t.driver == "redis" {
		res, err := query.NewRunner(st).Run(ctx, append([]string{"INFO"}, sections...))
		if err != nil {
			return nil, fmt.Errorf("inspect %q: %w", t.handle, redactErr(err, t.url))
		}
		info, _ := res.(string)
		return redisInfoTree(info), nil
	}

	subs := sections
	if len(subs) == 0 {
		subs = mongoInspectCmds
	}
	tree := make(map[string]any, len(subs))
	for _, sub := range subs {
		if !isMongoInspectCmd(sub) {
			return nil, fmt.Errorf("unknown inspect subcommand %q; want one of %s", sub, strings.Join(mongoInspectCmds, ", "))
		}
		if sub == "collStats" && t.collection() == "" {
			continue // needs a collection; skip in the same spirit as `inspect`
		}
		res, err := query.NewRunner(st).Run(ctx, []string{mongoInspectDoc(sub, t.collection())})
		if err != nil {
			return nil, fmt.Errorf("inspect %q %s: %w", t.handle, sub, redactErr(err, t.url))
		}
		tree[sub] = res
	}
	return tree, nil
}

// redisInfoTree turns an INFO reply into a JSON-ready tree of section -> key ->
// value for structural diffing.
func redisInfoTree(info string) map[string]any {
	parsed := parseRedisInfo(info)
	tree := make(map[string]any, len(parsed))
	for section, kv := range parsed {
		fields := make(map[string]any, len(kv))
		for k, v := range kv {
			fields[k] = v
		}
		tree[section] = fields
	}
	return tree
}

// diffSchema samples each source, infers a field/type shape, and diffs the
// shapes. It is allowed across drivers: the inferred vocabulary (integer/number/
// string(format)/map/array + required/optional + wildcards) measures logical
// shape, not per-driver introspection, so two backends compare meaningfully. Two
// backends that genuinely normalize a native type differently still diff — that
// is the JSON each serves back, and the format tags make the row legible.
func (r diffRun) diffSchema(ctx context.Context) ([]diff.Change, error) {
	a, b, err := r.schemaShapes(ctx)
	if err != nil {
		return nil, err
	}
	return diff.TreeOpt(a, b, r.opts), nil
}

// schemaShapes samples and infers both sources' comparable shapes. It is the
// shared collector behind the schema diff and the --patch schema layer.
func (r diffRun) schemaShapes(ctx context.Context) (a, b map[string]any, err error) {
	a, err = sampleShape(ctx, r.cfg, r.left, r.sample)
	if err != nil {
		return nil, nil, err
	}
	b, err = sampleShape(ctx, r.cfg, r.right, r.sample)
	if err != nil {
		return nil, nil, err
	}
	return a, b, nil
}

// layerCount counts how many of the three diff layers are selected.
func layerCount(dataMode, statsMode, schemaMode bool) int {
	n := 0
	for _, on := range []bool{dataMode, statsMode, schemaMode} {
		if on {
			n++
		}
	}
	return n
}

// patchLayer collects the one selected layer's two trees and renders their delta
// as an RFC 6902 JSON Patch. With neither statsMode nor schemaMode it is the data
// layer (the default), so onPage ticks the read spinner; the stats and schema
// layers ignore onPage.
func (r diffRun) patchLayer(ctx context.Context, statsMode, schemaMode bool, onPage func(int)) (json.RawMessage, error) {
	var a, b map[string]any
	var err error
	switch {
	case statsMode:
		a, b, err = r.statsTrees(ctx)
	case schemaMode:
		a, b, err = r.schemaShapes(ctx)
	default:
		a, b, err = r.readBoth(ctx, onPage)
	}
	if err != nil {
		return nil, err
	}
	return diff.Patch(a, b)
}

// renderPatch pretty-prints an RFC 6902 patch through the shared structured writer
// and applies the diff(1) exit contract: a non-empty patch returns errQuietExit
// (non-zero exit) while still printing, an empty patch prints [] and exits zero.
func renderPatch(out io.Writer, raw json.RawMessage) error {
	var ops []any
	if err := json.Unmarshal(raw, &ops); err != nil {
		return fmt.Errorf("decode json patch: %w", err)
	}
	if err := writeStructured(out, ops, false); err != nil {
		return err
	}
	if len(ops) > 0 {
		return errQuietExit
	}
	return nil
}

// sampleShape reads up to sample items from a source and infers its comparable
// shape. A sample of zero reads the whole keyspace.
func sampleShape(ctx context.Context, cfg *config, t sourceSpec, sample int) (map[string]any, error) {
	st, err := openStore(ctx, t.runConfig(cfg))
	if err != nil {
		return nil, redactErr(err, t.url)
	}
	defer func() { _ = st.Close() }()
	items, err := sampleItems(ctx, st, t.filter, sample, cfg.runOptions())
	if err != nil {
		return nil, fmt.Errorf("sample %q: %w", t.handle, redactErr(asSyntaxError(t.filter, err), t.url))
	}
	return shape.Infer(items).Comparable(), nil
}

// sampleItems reads up to sample items from an open store into a key->value map,
// stopping the scan once the cap is reached; a sample of zero reads the whole
// keyspace. The scan error is returned unwrapped so the caller can anchor it with
// its own source context. Shared by `diff --schema` and `schema`.
//
// A filter runs through the plain engine, not the keyed one: an inferred shape
// describes values and never keys, so a filter that fans one item out into
// several values (`.[] | .tags[]`) is a perfectly good question here even though
// a keyed read must refuse it. The emissions are collected under synthetic keys
// only because the shape inferrer takes a map.
func sampleItems(ctx context.Context, st store, filter string, sample int, opts query.RunOptions) (map[string]any, error) {
	items := map[string]any{}
	if filter != "" {
		n := 0
		err := query.NewJQEngine(st).Run(ctx, filter, opts, func(v any) error {
			items[strconv.Itoa(n)] = v
			n++
			if sampleFull(n, sample) {
				return errStopSampling
			}
			return nil
		})
		if err != nil && !errors.Is(err, errStopSampling) {
			return nil, err
		}
		return items, nil
	}
	err := st.ScanBatches(ctx, func(batch map[string]any) error {
		for k, v := range batch {
			items[k] = v
			if sampleFull(len(items), sample) {
				return errStopSampling
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopSampling) {
		return nil, err
	}
	return items, nil
}

// sampleFull reports whether count items reach the sample cap. A sample of zero
// means unbounded, so it is never full.
func sampleFull(count, sample int) bool {
	return sample > 0 && count >= sample
}

// report bundles the per-layer results a single `diff` run produced. The *Run
// flags record which layers were requested, so a selected layer that found no
// differences still renders "no differences" rather than vanishing (an empty
// diff is a nil slice, indistinguishable on its own from a layer not run).
type report struct {
	Data   []diff.ItemDelta `json:"data,omitempty"`
	Stats  []diff.Change    `json:"stats,omitempty"`
	Schema []diff.Change    `json:"schema,omitempty"`

	dataRun   bool
	statsRun  bool
	schemaRun bool
}

// empty reports whether every selected layer found no differences.
func (r report) empty() bool {
	return len(r.Data) == 0 && len(r.Stats) == 0 && len(r.Schema) == 0
}

// render writes the report as JSON, YAML, or a human diff.
func (r report) render(out io.Writer, left, right sourceSpec, jsonOut, yamlOut bool) error {
	if jsonOut || yamlOut {
		return writeStructured(out, r, yamlOut)
	}
	header := fmt.Sprintf("%s (%s)  →  %s (%s)", left.handle, left.driver, right.handle, right.driver)
	if _, err := fmt.Fprintf(out, "%s\n\n", pal.header.Sprint(header)); err != nil {
		return err
	}
	if r.dataRun {
		if err := renderItems(out, "data", r.Data); err != nil {
			return err
		}
	}
	if r.statsRun {
		if err := renderChanges(out, "stats", r.Stats); err != nil {
			return err
		}
	}
	if r.schemaRun {
		if err := renderChanges(out, "schema", r.Schema); err != nil {
			return err
		}
	}
	return nil
}

// renderItems writes a keyed data diff under a section heading.
func renderItems(out io.Writer, title string, deltas []diff.ItemDelta) error {
	if _, err := fmt.Fprintf(out, "%s\n", pal.header.Sprint("# "+title)); err != nil {
		return err
	}
	for _, d := range deltas {
		switch d.Op {
		case diff.OpChange:
			// The item header (symbol + key) takes the change color; the nested
			// field lines below color by their own op.
			header := colorLine(d.Op, fmt.Sprintf("%s %s", d.Op.Symbol(), d.Key))
			if _, err := fmt.Fprintf(out, "%s\n", header); err != nil {
				return err
			}
			for _, ch := range d.Changes {
				if err := renderChangeLine(out, "    ", ch); err != nil {
					return err
				}
			}
		default:
			// An add or remove row takes its op's color across the whole line.
			line := colorLine(d.Op, fmt.Sprintf("%s %s  %s", d.Op.Symbol(), d.Key, compact(sideValue(d.Op, d.Old, d.New))))
			if _, err := fmt.Fprintf(out, "%s\n", line); err != nil {
				return err
			}
		}
	}
	return writeSummary(out, diff.SummarizeItems(deltas))
}

// renderChanges writes a tree diff (stats or schema) under a section heading.
func renderChanges(out io.Writer, title string, changes []diff.Change) error {
	if _, err := fmt.Fprintf(out, "%s\n", pal.header.Sprint("# "+title)); err != nil {
		return err
	}
	for _, ch := range changes {
		if err := renderChangeLine(out, "", ch); err != nil {
			return err
		}
	}
	return writeSummary(out, diff.SummarizeChanges(changes))
}

// renderChangeLine writes one structural change, indented by prefix.
func renderChangeLine(out io.Writer, prefix string, ch diff.Change) error {
	path := strings.Join(ch.Path, ".")
	switch ch.Op {
	case diff.OpChange:
		// Symbol and path in the change color; the old value red and the new
		// value green, with the colon and arrow left plain.
		_, err := fmt.Fprintf(out, "%s%s: %s → %s\n",
			prefix,
			pal.change.Sprintf("%s %s", ch.Op.Symbol(), path),
			pal.remove.Sprint(compact(ch.Old)),
			pal.add.Sprint(compact(ch.New)))
		return err
	default:
		// An add or remove line takes its op's color across symbol, path, and value.
		line := colorLine(ch.Op, fmt.Sprintf("%s %s  %s", ch.Op.Symbol(), path, compact(sideValue(ch.Op, ch.Old, ch.New))))
		_, err := fmt.Fprintf(out, "%s%s\n", prefix, line)
		return err
	}
}

// writeSummary writes the footer counts for a section, each count colored by its
// operation.
func writeSummary(out io.Writer, s diff.Summary) error {
	if s.Empty() {
		_, err := fmt.Fprint(out, "no differences\n\n")
		return err
	}
	_, err := fmt.Fprintf(out, "%s, %s, %s\n\n",
		pal.add.Sprintf("%d added", s.Added),
		pal.remove.Sprintf("%d removed", s.Removed),
		pal.change.Sprintf("%d changed", s.Changed))
	return err
}

// sideValue returns the value that an add or remove carries.
func sideValue(op diff.Op, old, updated any) any {
	if op == diff.OpRemove {
		return old
	}
	return updated
}

// compact renders a value as one line of JSON for the human report.
func compact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// diffSpec resolves one diff operand. Diff compares whole keyspaces, so the
// operand is an address only; the spec's filter half arrives with --filter.
func diffSpec(cf *iqconfig.Config, arg string) (sourceSpec, error) {
	ep, err := resolveSourceSpec(cf, arg)
	if err != nil {
		return sourceSpec{}, err
	}
	return sourceSpec{endpoint: ep}, nil
}

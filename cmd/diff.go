package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	iqmongo "github.com/zsltg/iq/drivers/mongo"
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

// diffTarget is one side of a diff: the resolved handle, connection URL, dotted
// address override (may be ""), and canonical driver name.
type diffTarget struct {
	handle  string
	url     string
	address string
	driver  string
}

// collection returns the effective MongoDB collection for this target: the dotted
// address override when set, else the URL's ?collection= default.
func (t diffTarget) collection() string {
	if t.address != "" {
		return t.address
	}
	return iqmongo.CollectionFromURI(t.url)
}

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
	)
	long := "Compare two saved sources across the layers a schemaless store can meaningfully\n" +
		"compare. Selecting no layer defaults to --data; layers combine.\n\n" +
		"  --data    item-level diff: added / removed / changed, keyed by document _id\n" +
		"            (MongoDB) or key (Redis). Reads both keyspaces fully into memory,\n" +
		"            so it costs memory proportional to the two sources — a deliberate\n" +
		"            tradeoff, since an added/removed diff needs both key sets at once.\n" +
		"            Allowed across drivers (Mongo _id vs Redis key): useful for\n" +
		"            verifying a migration, but the identity match is only as meaningful\n" +
		"            as the keys lining up — a power-user tool, not a schema comparison.\n" +
		"  --stats   diff native introspection trees (MongoDB diagnostic commands, Redis\n" +
		"            INFO). Same driver only. --section narrows which sections.\n" +
		"  --schema  diff an inferred field->type shape sampled from each source. Allowed\n" +
		"            across drivers: the inferred vocabulary (integer/number/string(fmt)/\n" +
		"            map/array + required/optional) measures logical shape, not per-driver\n" +
		"            introspection. The shape is sampled (--sample) and inferred, never\n" +
		"            declared, so a wider sample yields a truer shape.\n\n" +
		"Use -j/--json or -y/--yaml for a machine-readable delta. diff exits non-zero when\n" +
		"the sources differ and zero when they match (diff(1)-style), so scripts can branch\n" +
		"on the exit status.\n\n" +
		"--set-arrays compares every array order-insensitively as a multiset (duplicates\n" +
		"counted), reporting membership deltas at the array's own path. --patch emits an\n" +
		"RFC 6902 JSON Patch instead of the human/JSON delta; it renders exactly one layer\n" +
		"(choose one of --data/--stats/--schema) and excludes --json, --yaml, and\n" +
		"--set-arrays."
	c := &cobra.Command{
		Use:   "diff <a> <b>",
		Short: "Compare two sources by data, stats, or inferred schema",
		Long:  long,
		Example: "  # Item-level diff (the default layer) of two collections in one source.\n" +
			"  $ iq diff shop.orders shop.users\n" +
			"\n" +
			"  # Across environments; compare shapes or native stats.\n" +
			"  $ iq diff prod/shop staging/shop --schema\n" +
			"  $ iq diff prod/shop staging/shop --stats\n" +
			"  $ iq diff prod/shop staging/shop -j",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			left, err := resolveDiffTarget(cf, args[0])
			if err != nil {
				return err
			}
			right, err := resolveDiffTarget(cf, args[1])
			if err != nil {
				return err
			}
			if patchOut && layerCount(dataMode, statsMode, schemaMode) > 1 {
				return errors.New("--patch renders one JSON Patch, so it needs a single layer; choose exactly one of --data, --stats, --schema")
			}
			if !dataMode && !statsMode && !schemaMode {
				dataMode = true
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
			defer cancel()

			if patchOut {
				// The one chosen layer, rendered as an RFC 6902 patch. --data reads
				// both keyspaces fully, so it carries the same spinner as the
				// human path; stop it before the error check so the line clears.
				meter := newProgressMeter(cmd.ErrOrStderr(), cfg.noProgress)
				raw, err := patchLayer(ctx, left, right, statsMode, schemaMode, sections, sample, meter.Tick)
				meter.Stop()
				if err != nil {
					return err
				}
				return renderPatch(cmd.OutOrStdout(), raw)
			}

			opts := diff.Options{SetArrays: setArrays}
			rep := report{dataRun: dataMode, statsRun: statsMode, schemaRun: schemaMode}
			if dataMode {
				// --data reads both keyspaces fully with no output until the
				// report renders, so a spinner fits cleanly. Stop it before the
				// error check and before render, so the line clears either way.
				meter := newProgressMeter(cmd.ErrOrStderr(), cfg.noProgress)
				d, err := diffData(ctx, left, right, meter.Tick, opts)
				meter.Stop()
				if err != nil {
					return err
				}
				rep.Data = d
			}
			if statsMode {
				s, err := diffStats(ctx, left, right, sections, opts)
				if err != nil {
					return err
				}
				rep.Stats = s
			}
			if schemaMode {
				s, err := diffSchema(ctx, left, right, sample, opts)
				if err != nil {
					return err
				}
				rep.Schema = s
			}

			if err := rep.render(cmd.OutOrStdout(), left, right, jsonOut, yamlOut); err != nil {
				return err
			}
			if !rep.empty() {
				return errQuietExit
			}
			return nil
		},
	}
	c.Flags().BoolVar(&dataMode, "data", false, "diff items key by key (default when no layer is chosen; cross-driver allowed)")
	c.Flags().BoolVar(&statsMode, "stats", false, "diff native introspection trees (same driver only)")
	c.Flags().BoolVar(&schemaMode, "schema", false, "diff an inferred field/type shape (cross-driver allowed)")
	c.Flags().StringArrayVar(&sections, "section", nil, "introspection section(s) for --stats (repeatable; default: the source's full set)")
	c.Flags().IntVar(&sample, "sample", 1000, "max items sampled per side for --schema (0 = all)")
	c.Flags().BoolVar(&setArrays, "set-arrays", false, "compare arrays order-insensitively as multisets (duplicates counted)")
	c.Flags().BoolVarP(&jsonOut, "json", "j", false, "emit machine-readable JSON")
	c.Flags().BoolVarP(&yamlOut, "yaml", "y", false, "emit machine-readable YAML")
	c.Flags().BoolVar(&patchOut, "patch", false, "emit an RFC 6902 JSON Patch (single layer only; excludes --json/--yaml/--set-arrays)")
	c.MarkFlagsMutuallyExclusive("json", "yaml", "patch")
	c.MarkFlagsMutuallyExclusive("patch", "set-arrays")
	return c
}

// resolveDiffTarget resolves a source handle to a connection target, splicing in
// a keyring secret when the source uses one.
func resolveDiffTarget(cf *iqconfig.Config, name string) (diffTarget, error) {
	base, addr, _ := splitSourceArg(cf, name)
	src, full, ok := cf.Resolve(base)
	if !ok {
		return diffTarget{}, fmt.Errorf("unknown source %q; run `iq ls`", base)
	}
	u, err := effectiveURL(src, full)
	if err != nil {
		return diffTarget{}, fmt.Errorf("source %q: %w", full, err)
	}
	if err := addressUnsupported(u, addr); err != nil {
		return diffTarget{}, err
	}
	return diffTarget{handle: full, url: u, address: addr, driver: driverName(u)}, nil
}

// diffData reads both keyspaces fully and diffs them key by key under opts.
func diffData(ctx context.Context, left, right diffTarget, onPage func(int), opts diff.Options) ([]diff.ItemDelta, error) {
	a, b, err := readBoth(ctx, left, right, onPage)
	if err != nil {
		return nil, err
	}
	return diff.KeyedOpt(a, b, opts), nil
}

// readBoth materializes both sources' whole keyspaces, ticking onPage per page.
// It is the shared collector behind the keyed data diff and the --patch data
// layer.
func readBoth(ctx context.Context, left, right diffTarget, onPage func(int)) (a, b map[string]any, err error) {
	a, err = readAll(ctx, left, onPage)
	if err != nil {
		return nil, nil, err
	}
	b, err = readAll(ctx, right, onPage)
	if err != nil {
		return nil, nil, err
	}
	return a, b, nil
}

// readAll materializes a source's whole keyspace as key -> value. A key repeated
// across scan pages (the store's weak scan guarantee) simply overwrites.
func readAll(ctx context.Context, t diffTarget, onPage func(int)) (map[string]any, error) {
	st, err := openStore(ctx, &config{url: t.url, address: t.address})
	if err != nil {
		return nil, redactErr(err, t.url)
	}
	defer func() { _ = st.Close() }()
	all := map[string]any{}
	err = st.ScanBatches(ctx, func(batch map[string]any) error {
		if onPage != nil {
			onPage(len(batch))
		}
		for k, v := range batch {
			all[k] = v
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", t.handle, redactErr(err, t.url))
	}
	return all, nil
}

// diffStats runs the same native introspection against both sources and diffs the
// replies under opts. Both sources must use the same driver.
func diffStats(ctx context.Context, left, right diffTarget, sections []string, opts diff.Options) ([]diff.Change, error) {
	a, b, err := statsTrees(ctx, left, right, sections)
	if err != nil {
		return nil, err
	}
	return diff.TreeOpt(a, b, opts), nil
}

// statsTrees collects both sources' native introspection trees, rejecting a
// cross-driver pair first. It is the shared collector behind the stats diff and
// the --patch stats layer.
func statsTrees(ctx context.Context, left, right diffTarget, sections []string) (a, b map[string]any, err error) {
	if left.driver != right.driver {
		return nil, nil, fmt.Errorf("stats diff needs two sources of the same driver; %q is %s and %q is %s", left.handle, left.driver, right.handle, right.driver)
	}
	a, err = collectInspect(ctx, left, sections)
	if err != nil {
		return nil, nil, err
	}
	b, err = collectInspect(ctx, right, sections)
	if err != nil {
		return nil, nil, err
	}
	return a, b, nil
}

// collectInspect runs the introspection commands for a source and returns the
// replies as one tree: Mongo keys each diagnostic reply by subcommand; Redis
// parses INFO into section -> key -> value.
func collectInspect(ctx context.Context, t diffTarget, sections []string) (map[string]any, error) {
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
func diffSchema(ctx context.Context, left, right diffTarget, sample int, opts diff.Options) ([]diff.Change, error) {
	a, b, err := schemaShapes(ctx, left, right, sample)
	if err != nil {
		return nil, err
	}
	return diff.TreeOpt(a, b, opts), nil
}

// schemaShapes samples and infers both sources' comparable shapes. It is the
// shared collector behind the schema diff and the --patch schema layer.
func schemaShapes(ctx context.Context, left, right diffTarget, sample int) (a, b map[string]any, err error) {
	a, err = sampleShape(ctx, left, sample)
	if err != nil {
		return nil, nil, err
	}
	b, err = sampleShape(ctx, right, sample)
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
func patchLayer(ctx context.Context, left, right diffTarget, statsMode, schemaMode bool, sections []string, sample int, onPage func(int)) (json.RawMessage, error) {
	var a, b map[string]any
	var err error
	switch {
	case statsMode:
		a, b, err = statsTrees(ctx, left, right, sections)
	case schemaMode:
		a, b, err = schemaShapes(ctx, left, right, sample)
	default:
		a, b, err = readBoth(ctx, left, right, onPage)
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
func sampleShape(ctx context.Context, t diffTarget, sample int) (map[string]any, error) {
	st, err := openStore(ctx, &config{url: t.url, address: t.address})
	if err != nil {
		return nil, redactErr(err, t.url)
	}
	defer func() { _ = st.Close() }()
	items, err := sampleItems(ctx, st, sample)
	if err != nil {
		return nil, fmt.Errorf("sample %q: %w", t.handle, redactErr(err, t.url))
	}
	return shape.Infer(items).Comparable(), nil
}

// sampleItems reads up to sample items from an open store into a key->value map,
// stopping the scan once the cap is reached; a sample of zero reads the whole
// keyspace. The scan error is returned unwrapped so the caller can anchor it with
// its own source context. Shared by `diff --schema` and `schema`.
func sampleItems(ctx context.Context, st store, sample int) (map[string]any, error) {
	items := map[string]any{}
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
func (r report) render(out io.Writer, left, right diffTarget, jsonOut, yamlOut bool) error {
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
			if _, err := fmt.Fprintf(out, "%s %s\n", diffSymbol(d.Op), d.Key); err != nil {
				return err
			}
			for _, ch := range d.Changes {
				if err := renderChangeLine(out, "    ", ch); err != nil {
					return err
				}
			}
		default:
			if _, err := fmt.Fprintf(out, "%s %s  %s\n", diffSymbol(d.Op), d.Key, compact(sideValue(d.Op, d.Old, d.New))); err != nil {
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
		_, err := fmt.Fprintf(out, "%s%s %s: %s → %s\n", prefix, diffSymbol(ch.Op), path, compact(ch.Old), compact(ch.New))
		return err
	default:
		_, err := fmt.Fprintf(out, "%s%s %s  %s\n", prefix, diffSymbol(ch.Op), path, compact(sideValue(ch.Op, ch.Old, ch.New)))
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

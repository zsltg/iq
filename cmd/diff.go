package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/diff"
	"github.com/zsltg/iq/internal/query"
)

// errQuietExit signals a non-zero exit with no stderr line: `diff --exit-code`
// returns it when differences exist, so scripts can branch on the exit status
// without the report being mistaken for an error. Execute recognizes it.
var errQuietExit = errors.New("differences found")

// errStopSampling stops a ScanBatches walk once the schema sample is full. It is
// a control signal, never surfaced.
var errStopSampling = errors.New("sample complete")

// diffTarget is one side of a diff: the resolved handle, connection URL,
// collection, and driver scheme.
type diffTarget struct {
	handle     string
	url        string
	collection string
	scheme     string
}

// newDiffCmd builds `iq diff <a> <b>`: compare two saved sources. --data (the
// default) diffs items key by key; --stats diffs native introspection; --schema
// diffs an inferred field/type shape. Layers combine. Bounded by --timeout.
func newDiffCmd(cfg *config) *cobra.Command {
	var (
		dataMode, statsMode, schemaMode bool
		jsonOut, exitCode               bool
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
		"  --schema  diff an inferred field->type shape sampled from each source. Same\n" +
		"            driver only. The shape is sampled (--sample) and inferred, never\n" +
		"            declared, so a wider sample yields a truer shape.\n\n" +
		"Use --json for a machine-readable delta, or --exit-code to exit non-zero when\n" +
		"differences exist (for scripts); by default diff exits zero whether or not the\n" +
		"sources differ."
	c := &cobra.Command{
		Use:   "diff <a> <b>",
		Short: "Compare two sources by data, stats, or inferred schema",
		Long:  long,
		Args:  cobra.ExactArgs(2),
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
			if !dataMode && !statsMode && !schemaMode {
				dataMode = true
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
			defer cancel()

			rep := report{dataRun: dataMode, statsRun: statsMode, schemaRun: schemaMode}
			if dataMode {
				// --data reads both keyspaces fully with no output until the
				// report renders, so a spinner fits cleanly. Stop it before the
				// error check and before render, so the line clears either way.
				meter := newProgressMeter(cmd.ErrOrStderr(), cfg.noProgress)
				d, err := diffData(ctx, left, right, meter.Tick)
				meter.Stop()
				if err != nil {
					return err
				}
				rep.Data = d
			}
			if statsMode {
				s, err := diffStats(ctx, left, right, sections)
				if err != nil {
					return err
				}
				rep.Stats = s
			}
			if schemaMode {
				s, err := diffSchema(ctx, left, right, sample)
				if err != nil {
					return err
				}
				rep.Schema = s
			}

			if err := rep.render(cmd.OutOrStdout(), left, right, jsonOut); err != nil {
				return err
			}
			if exitCode && !rep.empty() {
				return errQuietExit
			}
			return nil
		},
	}
	c.Flags().BoolVar(&dataMode, "data", false, "diff items key by key (default when no layer is chosen; cross-driver allowed)")
	c.Flags().BoolVar(&statsMode, "stats", false, "diff native introspection trees (same driver only)")
	c.Flags().BoolVar(&schemaMode, "schema", false, "diff an inferred field/type shape (same driver only)")
	c.Flags().StringArrayVar(&sections, "section", nil, "introspection section(s) for --stats (repeatable; default: the source's full set)")
	c.Flags().IntVar(&sample, "sample", 1000, "max items sampled per side for --schema (0 = all)")
	c.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	c.Flags().BoolVar(&exitCode, "exit-code", false, "exit non-zero when differences exist (otherwise always zero)")
	return c
}

// resolveDiffTarget resolves a source handle to a connection target, splicing in
// a keyring secret when the source uses one.
func resolveDiffTarget(cf *iqconfig.Config, name string) (diffTarget, error) {
	src, full, ok := cf.Resolve(name)
	if !ok {
		return diffTarget{}, fmt.Errorf("unknown source %q; run `iq ls`", name)
	}
	u, err := effectiveURL(src, full)
	if err != nil {
		return diffTarget{}, fmt.Errorf("source %q: %w", full, err)
	}
	return diffTarget{handle: full, url: u, collection: src.Collection, scheme: schemeOf(u)}, nil
}

// diffData reads both keyspaces fully and diffs them key by key.
func diffData(ctx context.Context, left, right diffTarget, onPage func(int)) ([]diff.ItemDelta, error) {
	a, err := readAll(ctx, left, onPage)
	if err != nil {
		return nil, err
	}
	b, err := readAll(ctx, right, onPage)
	if err != nil {
		return nil, err
	}
	return diff.Keyed(a, b), nil
}

// readAll materializes a source's whole keyspace as key -> value. A key repeated
// across scan pages (the store's weak scan guarantee) simply overwrites.
func readAll(ctx context.Context, t diffTarget, onPage func(int)) (map[string]any, error) {
	st, err := openStore(ctx, &config{url: t.url, collection: t.collection})
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
// replies. Both sources must use the same driver.
func diffStats(ctx context.Context, left, right diffTarget, sections []string) ([]diff.Change, error) {
	if left.scheme != right.scheme {
		return nil, fmt.Errorf("stats diff needs two sources of the same driver; %q is %s and %q is %s", left.handle, left.scheme, right.handle, right.scheme)
	}
	a, err := collectInspect(ctx, left, sections)
	if err != nil {
		return nil, err
	}
	b, err := collectInspect(ctx, right, sections)
	if err != nil {
		return nil, err
	}
	return diff.Tree(a, b), nil
}

// collectInspect runs the introspection commands for a source and returns the
// replies as one tree: Mongo keys each diagnostic reply by subcommand; Redis
// parses INFO into section -> key -> value.
func collectInspect(ctx context.Context, t diffTarget, sections []string) (map[string]any, error) {
	st, err := openStore(ctx, &config{url: t.url, collection: t.collection})
	if err != nil {
		return nil, redactErr(err, t.url)
	}
	defer func() { _ = st.Close() }()

	if strings.HasPrefix(t.scheme, "redis") {
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
		if sub == "collStats" && t.collection == "" {
			continue // needs a collection; skip in the same spirit as `inspect`
		}
		res, err := query.NewRunner(st).Run(ctx, []string{mongoInspectDoc(sub, t.collection)})
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
// shapes. Both sources must use the same driver.
func diffSchema(ctx context.Context, left, right diffTarget, sample int) ([]diff.Change, error) {
	if left.scheme != right.scheme {
		return nil, fmt.Errorf("schema diff needs two sources of the same driver; %q is %s and %q is %s", left.handle, left.scheme, right.handle, right.scheme)
	}
	a, err := sampleShape(ctx, left, sample)
	if err != nil {
		return nil, err
	}
	b, err := sampleShape(ctx, right, sample)
	if err != nil {
		return nil, err
	}
	return diff.Tree(a, b), nil
}

// sampleShape reads up to sample items from a source and infers its shape. A
// sample of zero reads the whole keyspace.
func sampleShape(ctx context.Context, t diffTarget, sample int) (map[string]any, error) {
	st, err := openStore(ctx, &config{url: t.url, collection: t.collection})
	if err != nil {
		return nil, redactErr(err, t.url)
	}
	defer func() { _ = st.Close() }()
	items := map[string]any{}
	err = st.ScanBatches(ctx, func(batch map[string]any) error {
		for k, v := range batch {
			items[k] = v
			if sampleFull(len(items), sample) {
				return errStopSampling
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopSampling) {
		return nil, fmt.Errorf("sample %q: %w", t.handle, redactErr(err, t.url))
	}
	return diff.Infer(items), nil
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

// render writes the report as JSON or as a human diff.
func (r report) render(out io.Writer, left, right diffTarget, jsonOut bool) error {
	if jsonOut {
		return newJSONEncoder(out, true).Encode(r)
	}
	header := fmt.Sprintf("%s (%s)  →  %s (%s)", left.handle, left.scheme, right.handle, right.scheme)
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

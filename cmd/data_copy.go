package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// dataCopyPageSize bounds how many records a copy buffers before a batched write,
// trading round-trips against memory; it is also the batch size --explain reports.
const dataCopyPageSize = 500

// copyOptions collects the copy subcommand's local flags.
type copyOptions struct {
	noOverwrite bool
	replace     bool
	force       bool
	filter      string
	key         string
	keyField    string
	keyPrefix   string
	typ         string
}

// newDataCopyCmd builds `iq data copy <src> [dst]`: the single data-movement
// command. Each endpoint is a saved source (`name[.coll]`) or a file path/`-`; an
// omitted dst is stdout. A source→file copy is a typed JSONL dump, a file→source
// copy restores one (or imports foreign JSON with --key-field), and source→source
// is a live, key-preserving copy, cross-driver included. --filter transforms each
// item during the copy, re-keying by the source key or --key.
func newDataCopyCmd(cfg *config, df *dataFlags) *cobra.Command {
	var opts copyOptions
	c := &cobra.Command{
		Use:   "copy <src> [dst]",
		Short: "Copy items between sources, with an optional transform",
		Long: "Copy items between saved sources. Each endpoint is a source handle (`name[.coll]`);\n" +
			"a dump file is a `file://` source, registered with `iq add`. Write a file with -o,\n" +
			"and read/write stdio with `-`.\n\n" +
			"  src     -> dst      live, key-preserving copy (cross-driver allowed)\n" +
			"  src     -> (none)   dump to stdout — add -o FILE for a typed JSONL dump on disk\n" +
			"  file://.. -> dst    restore a dump (RDB/BSON/mongoexport/JSONL) into a live source\n" +
			"  -       -> dst      import foreign JSON piped on stdin with --key-field\n\n" +
			"--filter '<jq>' transforms each item; its output is written under the item's\n" +
			"own key (1:1) or the key from --key/--key-field. Existing keys are overwritten\n" +
			"(upsert) unless --no-overwrite; --replace empties the destination first.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDataCopy(cmd, cfg, df, &opts, args)
		},
	}
	c.Flags().BoolVar(&opts.noOverwrite, "no-overwrite", false, "insert only; skip keys that already exist")
	c.Flags().BoolVar(&opts.replace, "replace", false, "empty the destination before copying (needs confirmation)")
	c.Flags().BoolVar(&opts.force, "force", false, "skip the confirmation prompt for --replace")
	c.Flags().StringVar(&opts.filter, "filter", "", "jq expression applied to each item before writing")
	c.Flags().StringVar(&opts.key, "key", "", "jq expression yielding each written item's key")
	c.Flags().StringVar(&opts.keyField, "key-field", "", "object field to take the destination key from (foreign JSON)")
	c.Flags().StringVar(&opts.keyPrefix, "key-prefix", "", "string prepended to every destination key")
	c.Flags().StringVar(&opts.typ, "type", "", "native type stamped on a reshaped value (e.g. hash, list, json)")
	return c
}

// runDataCopy resolves both endpoints, builds the transform, and either prints the
// plan (--explain) or streams the copy through a Copier, reporting the outcome.
func runDataCopy(cmd *cobra.Command, cfg *config, df *dataFlags, opts *copyOptions, args []string) error {
	if err := df.validate(); err != nil {
		return err
	}
	if opts.noOverwrite && opts.replace {
		return errors.New("--no-overwrite and --replace are mutually exclusive")
	}
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	src, err := resolveEndpoint(cf, args[0], false)
	if err != nil {
		return err
	}
	dstArg := ""
	if len(args) > 1 {
		dstArg = args[1]
	}
	dst, err := resolveEndpoint(cf, dstArg, true)
	if err != nil {
		return err
	}
	if src.isFile && dst.isFile {
		return errors.New("at least one endpoint must be a source; file-to-file is not a copy")
	}

	mode := query.Upsert
	if opts.noOverwrite {
		mode = query.InsertOnly
	}

	if df.explain {
		out := renderCopyPlan(src, dst, mode)
		_, err := fmt.Fprint(cmd.OutOrStdout(), out)
		return err
	}

	transform, err := query.NewTransform(query.TransformOptions{
		Filter: opts.filter, Key: opts.key, KeyField: opts.keyField,
		KeyPrefix: opts.keyPrefix, Type: opts.typ,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
	defer cancel()

	recSrc, closeSrc, err := openCopySource(cmd, ctx, src, opts)
	if err != nil {
		return err
	}
	defer func() { _ = closeSrc() }()

	putter, clearer, closeDst, err := openCopyDest(cmd, ctx, dst, opts)
	if err != nil {
		return err
	}
	defer func() { _ = closeDst() }()

	if opts.replace && !df.dryRun {
		if err := confirmDestruction(cmd, fmt.Sprintf("clear %s before copy", dst.label()), opts.force); err != nil {
			return err
		}
		if err := clearer.Clear(ctx); err != nil {
			return redactErr(err, dst.url)
		}
	}

	copier := query.Copier{Dst: putter, Mode: mode, PageSize: dataCopyPageSize, Transform: transform}
	stat, err := copier.Copy(ctx, recSrc, df.dryRun)
	if err != nil {
		return redactErr(err, dst.url)
	}
	reportCopy(cmd, dst, stat, df.dryRun)
	return nil
}

// openCopySource opens the read side as a RecordSource: a source's typed scan, or
// a JSONL file. Foreign JSON (plain mode) is signalled by --key-field.
func openCopySource(cmd *cobra.Command, ctx context.Context, src endpoint, opts *copyOptions) (query.RecordSource, func() error, error) {
	if src.isFile {
		r, closeIn, err := openFileInput(cmd, src.path)
		if err != nil {
			return nil, nil, err
		}
		plain := opts.keyField != ""
		return query.JSONLSource(r, dataCopyPageSize, plain), closeIn, nil
	}
	st, err := openStore(ctx, &config{url: src.url, collection: src.collection})
	if err != nil {
		return nil, nil, redactErr(err, src.url)
	}
	tr, ok := st.(query.TypedReader)
	if !ok {
		_ = st.Close()
		return nil, nil, fmt.Errorf("source %s (%s) cannot be copied from", src.handle, src.driver)
	}
	return tr.TypedScan, st.Close, nil
}

// openCopyDest opens the write side as a Putter (and a Clearer when --replace needs
// one): a source's write adapter, or a JSONL file/stdout sink. Write-mode flags are
// invalid for a file destination, which always writes.
func openCopyDest(cmd *cobra.Command, ctx context.Context, dst endpoint, opts *copyOptions) (query.Putter, query.Clearer, func() error, error) {
	if dst.isFile {
		if opts.noOverwrite || opts.replace {
			return nil, nil, nil, errors.New("--no-overwrite/--replace apply only to a source destination, not a file")
		}
		w, closeOut, err := openFileOutput(cmd, dst.path)
		if err != nil {
			return nil, nil, nil, err
		}
		return jsonlPutter{w: w}, nil, closeOut, nil
	}
	st, err := openStore(ctx, &config{url: dst.url, collection: dst.collection})
	if err != nil {
		return nil, nil, nil, redactErr(err, dst.url)
	}
	putter, ok := st.(query.Putter)
	if !ok {
		_ = st.Close()
		return nil, nil, nil, fmt.Errorf("destination %s (%s) cannot be written to", dst.handle, dst.driver)
	}
	var clearer query.Clearer
	if opts.replace {
		clearer, ok = st.(query.Clearer)
		if !ok {
			_ = st.Close()
			return nil, nil, nil, fmt.Errorf("--replace: destination %s (%s) cannot be cleared", dst.handle, dst.driver)
		}
	}
	return putter, clearer, st.Close, nil
}

// reportCopy writes the outcome to stderr, so a file/stdout dump on stdout stays
// clean. A dry run says "would".
func reportCopy(cmd *cobra.Command, dst endpoint, stat query.WriteStat, dry bool) {
	verb := "copied"
	if dry {
		verb = "would copy"
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s %d item(s) to %s (new %d, overwritten %d, skipped %d)\n",
		verb, stat.Written+stat.Overwritten, dst.label(), stat.Written, stat.Overwritten, stat.Skipped)
}

// renderCopyPlan builds the static --explain output for a copy: the endpoints and,
// for a source endpoint, the driver's read/write operations from its describers —
// all without connecting.
func renderCopyPlan(src, dst endpoint, mode query.WriteMode) string {
	var b strings.Builder
	b.WriteString(planHeader("copy plan") + "\n")
	writePlanLine(&b, "from", src.label())
	writePlanLine(&b, "to", dst.label())

	writePlanSection(&b, "read")
	if src.isFile {
		b.WriteString("  decode typed JSONL\n")
	} else if d, ok := driverForScheme(schemeOf(src.url)); ok && d.explainPlan != nil {
		for _, op := range d.explainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, false).Ops {
			b.WriteString("  " + op + "\n")
		}
	}

	writePlanSection(&b, "write")
	if dst.isFile {
		b.WriteString("  encode typed JSONL\n")
	} else if d, ok := driverForScheme(schemeOf(dst.url)); ok && d.explainWrite != nil {
		for _, op := range d.explainWrite(mode).Ops {
			b.WriteString("  " + op + "\n")
		}
	}
	return b.String()
}

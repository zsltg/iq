package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	iqfile "github.com/zsltg/iq/drivers/file"
	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
)

// movePageSize bounds how many records a move buffers before a batched write.
const movePageSize = 500

// runMove is the write side of the default command. With --insert it copies each
// source item into a destination source; with --typed it emits {key,type,value}
// records — a re-importable dump. The optional positional filter transforms each
// item (keys preserved via the source's typed scan), so a straight copy or dump
// needs no filter. This is the sq-style replacement for `iq data copy`.
func runMove(cmd *cobra.Command, cfg *config, filter string) error {
	if cfg.noOverwrite && cfg.replace {
		return errors.New("--no-overwrite and --replace are mutually exclusive")
	}
	if cfg.typed && (cfg.noOverwrite || cfg.replace || cfg.force || cfg.dryRun) {
		return errors.New("--no-overwrite/--replace/--force/--dry-run apply to --insert, not --typed")
	}

	transform, err := query.NewTransform(query.TransformOptions{
		Filter: filter, Key: cfg.moveKey, KeyField: cfg.keyField,
		KeyPrefix: cfg.keyPrefix, Type: cfg.moveType,
	})
	if err != nil {
		return err
	}

	if cfg.explain {
		return renderMoveExplain(cmd, cfg)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
	defer cancel()

	recSrc, closeSrc, _, err := openMoveSource(cmd, ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = closeSrc() }()

	if cfg.typed {
		return runTypedDump(cmd, ctx, cfg, recSrc, transform)
	}
	return runInsert(cmd, ctx, cfg, recSrc, transform)
}

// openMoveSource opens the read side as a RecordSource: piped stdin when no --src is
// given, otherwise the selected source's typed scan. It returns a label for the
// source used in the outcome/plan.
func openMoveSource(cmd *cobra.Command, ctx context.Context, cfg *config) (query.RecordSource, func() error, string, error) {
	if cfg.src == "" && !stdinIsTerminal() {
		r, closeIn, err := openFileInput(cmd, "-")
		if err != nil {
			return nil, nil, "", err
		}
		src, err := stdinRecordSource(r, cfg)
		if err != nil {
			_ = closeIn()
			return nil, nil, "", err
		}
		return src, closeIn, "stdin", nil
	}
	if err := resolveSource(cfg); err != nil {
		return nil, nil, "", err
	}
	st, err := openStore(ctx, cfg)
	if err != nil {
		return nil, nil, "", redactErr(err, cfg.url)
	}
	tr, ok := st.(query.TypedReader)
	if !ok {
		_ = st.Close()
		return nil, nil, "", fmt.Errorf("source %s (%s) cannot be read for a move", cfg.handle, driverName(cfg.url))
	}
	return tr.TypedScan, st.Close, cfg.handle, nil
}

// stdinRecordSource decodes piped stdin into records. Foreign JSON (unkeyed) is
// signalled by --key-field; otherwise the stream defaults to iq's typed JSONL dump
// unless --from-format names another (rdb/bson/mongoexport/yaml/json).
func stdinRecordSource(r io.Reader, cfg *config) (query.RecordSource, error) {
	if cfg.keyField != "" && cfg.fromFormat == "" {
		return query.JSONLSource(r, movePageSize, true), nil
	}
	format := iqfile.FormatJSONL
	if cfg.fromFormat != "" {
		f, err := iqfile.ParseFormat(cfg.fromFormat)
		if err != nil {
			return nil, err
		}
		format = f
	}
	return iqfile.RecordSourceFor(r, format, cfg.decimalMode, iqfile.Hints{})
}

// runInsert copies the source records into the --insert destination through a
// Putter, honoring the write mode and --replace, and reports the outcome.
func runInsert(cmd *cobra.Command, ctx context.Context, cfg *config, recSrc query.RecordSource, transform func(query.Record) ([]query.Record, error)) error {
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	dst, err := resolveEndpoint(cf, cfg.insert, true)
	if err != nil {
		return err
	}
	if dst.isFile {
		return errors.New("--insert must name a saved source; to write a file dump use --typed with -o")
	}
	mode := query.Upsert
	if cfg.noOverwrite {
		mode = query.InsertOnly
	}

	st, err := openStore(ctx, &config{url: dst.url, address: dst.address})
	if err != nil {
		return redactErr(err, dst.url)
	}
	defer func() { _ = st.Close() }()
	putter, ok := st.(query.Putter)
	if !ok {
		return fmt.Errorf("destination %s (%s) cannot be written to", dst.handle, dst.driver)
	}
	if cfg.replace {
		clearer, ok := st.(query.Clearer)
		if !ok {
			return fmt.Errorf("--replace: destination %s (%s) cannot be cleared", dst.handle, dst.driver)
		}
		if !cfg.dryRun {
			if err := confirmDestruction(cmd, fmt.Sprintf("clear %s before writing", dst.label()), cfg.force); err != nil {
				return err
			}
			if err := clearer.Clear(ctx); err != nil {
				return redactErr(err, dst.url)
			}
		}
	}

	copier := query.Copier{Dst: putter, Mode: mode, PageSize: movePageSize, Transform: transform}
	stat, err := copier.Copy(ctx, recSrc, cfg.dryRun)
	if err != nil {
		return redactErr(err, dst.url)
	}
	reportMove(cmd, dst.label(), stat, cfg.dryRun)
	return nil
}

// runTypedDump streams the source records to stdout/-o as {key,type,value} records
// in the selected structured format (jsonl by default), a re-importable dump.
func runTypedDump(cmd *cobra.Command, ctx context.Context, cfg *config, recSrc query.RecordSource, transform func(query.Record) ([]query.Record, error)) error {
	fm, err := selectTypedFormat(cfg)
	if err != nil {
		return err
	}
	f := newFormatter(fm, cmd.OutOrStdout(), cfg.compact)
	copier := query.Copier{Dst: typedPutter{f: f}, Mode: query.Upsert, PageSize: movePageSize, Transform: transform}
	if _, err := copier.Copy(ctx, recSrc, false); err != nil {
		return err
	}
	return f.flush()
}

// typedRecord is the {key,type,value} envelope emitted by --typed; it matches the
// on-disk dump shape (internal/query dumpRecord) so a dump re-imports.
type typedRecord struct {
	Key   string `json:"key" yaml:"key"`
	Type  string `json:"type" yaml:"type"`
	Value any    `json:"value" yaml:"value"`
}

// typedPutter is a Putter that renders each record through an output formatter,
// letting --typed reuse the Copier pipeline (source scan + transform) to emit a dump.
type typedPutter struct{ f formatter }

func (p typedPutter) Put(_ context.Context, batch []query.Record, _ query.WriteMode) (query.WriteStat, error) {
	for _, r := range batch {
		if err := p.f.emit(typedRecord{Key: r.Key, Type: r.Type, Value: r.Value}); err != nil {
			return query.WriteStat{}, err
		}
	}
	return query.WriteStat{Written: len(batch)}, nil
}

// selectTypedFormat resolves the record serialization for --typed: the chosen
// structured format, or jsonl when none is set. The scalar "values" rendering
// cannot represent a record and is rejected.
func selectTypedFormat(cfg *config) (outputFormat, error) {
	if !anyFormatFlag(cfg) {
		return formatJSONL, nil
	}
	fm, err := selectFormat(cfg)
	if err != nil {
		return "", err
	}
	if fm == formatValues {
		return "", errors.New("--typed cannot use --raw/--values; choose --jsonl (default), --jsona, or --yaml")
	}
	if fm == formatParquet {
		return "", errors.New("--typed cannot use --format parquet; run the query without --typed to export a columnar file, or choose --jsonl (default), --jsona, or --yaml")
	}
	return fm, nil
}

// anyFormatFlag reports whether the invocation set any output-format flag.
func anyFormatFlag(cfg *config) bool {
	return cfg.format != "" || cfg.json || cfg.jsonArray || cfg.jsonl || cfg.yaml || cfg.raw
}

// reportMove writes the outcome to stderr, so a --typed dump on stdout stays clean.
func reportMove(cmd *cobra.Command, dst string, stat query.WriteStat, dry bool) {
	verb := "wrote"
	if dry {
		verb = "would write"
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s %d item(s) to %s (new %d, overwritten %d, skipped %d)\n",
		verb, stat.Written+stat.Overwritten, dst, stat.Written, stat.Overwritten, stat.Skipped)
}

// renderMoveExplain prints the move plan without connecting: the endpoints and the
// destination driver's write operations.
func renderMoveExplain(cmd *cobra.Command, cfg *config) error {
	var b strings.Builder
	b.WriteString(planHeader("move plan") + "\n")

	from := "stdin"
	if cfg.src != "" {
		from = cfg.src
	}
	writePlanLine(&b, "from", from)

	mode := query.Upsert
	if cfg.noOverwrite {
		mode = query.InsertOnly
	}
	if cfg.typed {
		writePlanLine(&b, "to", "typed dump (stdout/-o)")
		writePlanSection(&b, "write")
		b.WriteString("  encode typed records\n")
	} else {
		cf, err := iqconfig.Load()
		if err != nil {
			return err
		}
		dst, err := resolveEndpoint(cf, cfg.insert, true)
		if err != nil {
			return err
		}
		writePlanLine(&b, "to", dst.label())
		writePlanSection(&b, "write")
		if d, ok := driverForScheme(schemeOf(dst.url)); ok && d.explainWrite != nil {
			for _, op := range d.explainWrite(mode).Ops {
				b.WriteString("  " + op + "\n")
			}
		}
	}
	_, err := fmt.Fprint(cmd.OutOrStdout(), b.String())
	return err
}

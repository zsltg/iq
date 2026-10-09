package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
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
	if err := validateMoveFlags(cfg); err != nil {
		return err
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

// validateMoveFlags rejects the flag mixes that a move cannot honor, in order:
// --no-overwrite with --replace, then --typed with an insert-only flag.
func validateMoveFlags(cfg *config) error {
	if cfg.noOverwrite && cfg.replace {
		return errors.New("--no-overwrite and --replace are mutually exclusive")
	}
	if cfg.typed && anyOn(cfg.noOverwrite, cfg.replace, cfg.force, cfg.dryRun) {
		return errors.New("--no-overwrite/--replace/--force/--dry-run apply to --insert, not --typed")
	}
	return nil
}

// anyOn reports whether at least one of flags is true.
func anyOn(flags ...bool) bool {
	return slices.Contains(flags, true)
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
	tr, closeSrc, err := openTypedSource(ctx, cfg)
	if err != nil {
		return nil, nil, "", err
	}
	return tr.TypedScan, closeSrc, cfg.handle, nil
}

// openTypedSource opens the store of cfg as a typed reader, for a move or an
// iq_insert call. The error has the connection URI redacted. The caller closes
// the store with the returned function.
func openTypedSource(ctx context.Context, cfg *config) (query.TypedReader, func() error, error) {
	st, err := openStore(ctx, cfg)
	if err != nil {
		return nil, nil, redactErr(err, cfg.url)
	}
	tr, ok := st.(query.TypedReader)
	if !ok {
		_ = st.Close()
		return nil, nil, fmt.Errorf("source %s (%s) cannot be read for a move", cfg.handle, driverName(cfg.url))
	}
	return tr, st.Close, nil
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

// insertRequest is the write side of a move, stated without any CLI type so both
// `iq --insert` and the MCP iq_insert tool drive one implementation: the
// destination handle, the write mode, whether the destination is emptied first,
// whether the run only reports, and how a destructive step is confirmed.
type insertRequest struct {
	dst     string
	mode    query.WriteMode
	replace bool
	dryRun  bool
	// confirm gates emptying the destination for replace. It returns nil to
	// proceed and an error to abort; it is never called on a dry run.
	confirm func(action string) error
}

// runInsert copies the source records into the --insert destination through a
// Putter, honoring the write mode and --replace, and reports the outcome.
func runInsert(cmd *cobra.Command, ctx context.Context, cfg *config, recSrc query.RecordSource, transform func(query.Record) ([]query.Record, error)) error {
	label, stat, err := applyInsert(ctx, insertRequestFor(cmd, cfg), recSrc, transform)
	if err != nil {
		return err
	}
	reportMove(cmd, label, stat, cfg.dryRun)
	return nil
}

// insertRequestFor states the CLI's --insert flags as an insertRequest: the
// destination, the write mode --no-overwrite selects, --replace, --dry-run, and
// the interactive confirmation --force short-circuits.
func insertRequestFor(cmd *cobra.Command, cfg *config) insertRequest {
	return insertRequest{
		dst: cfg.insert, mode: writeModeFor(cfg), replace: cfg.replace, dryRun: cfg.dryRun,
		confirm: func(action string) error { return confirmDestruction(cmd, action, cfg.force) },
	}
}

// writeModeFor returns the write mode that cfg selects: insert-only for
// --no-overwrite, upsert otherwise.
func writeModeFor(cfg *config) query.WriteMode {
	if cfg.noOverwrite {
		return query.InsertOnly
	}
	return query.Upsert
}

// applyInsert resolves the destination, opens it, empties it when replace is set
// (after confirmation), and copies the records through the Putter. It returns the
// destination label for the outcome line and the write statistics. Every error
// crossing out of it has the connection URI redacted.
func applyInsert(ctx context.Context, req insertRequest, recSrc query.RecordSource, transform func(query.Record) ([]query.Record, error)) (string, query.WriteStat, error) {
	dst, err := resolveInsertTarget(req.dst)
	if err != nil {
		return "", query.WriteStat{}, err
	}
	st, err := openStore(ctx, &config{url: dst.url, address: dst.address})
	if err != nil {
		return "", query.WriteStat{}, redactErr(err, dst.url)
	}
	defer func() { _ = st.Close() }()
	putter, err := insertPutter(st, dst)
	if err != nil {
		return "", query.WriteStat{}, err
	}
	if req.replace {
		if err := clearForReplace(ctx, st, dst, req); err != nil {
			return "", query.WriteStat{}, err
		}
	}

	copier := query.Copier{Dst: putter, Mode: req.mode, PageSize: movePageSize, Transform: transform}
	stat, err := copier.Copy(ctx, recSrc, req.dryRun)
	if err != nil {
		return "", query.WriteStat{}, redactErr(err, dst.url)
	}
	return dst.label(), stat, nil
}

// resolveInsertTarget loads the registry and resolves the --insert destination to
// a saved source. A file endpoint is refused.
func resolveInsertTarget(name string) (endpoint, error) {
	cf, err := iqconfig.Load()
	if err != nil {
		return endpoint{}, err
	}
	dst, err := resolveEndpoint(cf, name, true)
	if err != nil {
		return endpoint{}, err
	}
	if dst.isFile {
		return endpoint{}, errors.New("--insert must name a saved source; to write a file dump use --typed with -o")
	}
	return dst, nil
}

// insertPutter returns st as a Putter, or the error that names the destination.
func insertPutter(st store, dst endpoint) (query.Putter, error) {
	putter, ok := st.(query.Putter)
	if !ok {
		return nil, fmt.Errorf("destination %s (%s) cannot be written to", dst.handle, dst.driver)
	}
	return putter, nil
}

// clearForReplace empties the destination for --replace. A destination that cannot
// be cleared is an error even on a dry run. A real run asks for the confirmation
// first.
func clearForReplace(ctx context.Context, st store, dst endpoint, req insertRequest) error {
	clearer, ok := st.(query.Clearer)
	if !ok {
		return fmt.Errorf("--replace: destination %s (%s) cannot be cleared", dst.handle, dst.driver)
	}
	if req.dryRun {
		return nil
	}
	if err := req.confirm(fmt.Sprintf("clear %s before writing", dst.label())); err != nil {
		return err
	}
	if err := clearer.Clear(ctx); err != nil {
		return redactErr(err, dst.url)
	}
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

// typedFormatChoices names the renderings a typed dump can use, for the rejection
// messages below; every one of them re-imports through a file:// source.
const typedFormatChoices = "choose --jsonl (default), --json, --jsona, or --yaml"

// selectTypedFormat resolves the record serialization for --typed: the chosen
// structured format, or jsonl when none is set. A dump exists to be re-imported,
// so a rendering that cannot carry a {key,type,value} record back is rejected:
// the scalar "values" rendering cannot represent a record, parquet is columnar,
// and gron flattens the record to assignment statements that no source decodes.
func selectTypedFormat(cfg *config) (outputFormat, error) {
	if !anyFormatFlag(cfg) {
		return formatJSONL, nil
	}
	fm, err := selectFormat(cfg)
	if err != nil {
		return "", err
	}
	if fm == formatValues {
		return "", errors.New("--typed cannot use --raw/--values; " + typedFormatChoices)
	}
	if fm == formatParquet {
		return "", errors.New("--typed cannot use --format parquet; run the query without --typed to export a columnar file, or " + typedFormatChoices)
	}
	if fm == formatGron || fm == formatGronArray {
		return "", errors.New("--typed cannot use --gron/--grona; a gron dump does not re-import, so run the query without --typed to grep the rendering, or " + typedFormatChoices)
	}
	return fm, nil
}

// anyFormatFlag reports whether the invocation set any output-format flag. Every
// flag selectFormat reads is listed, so a rendering --typed rejects reaches that
// rejection instead of falling through to the jsonl default.
func anyFormatFlag(cfg *config) bool {
	return cfg.format != "" ||
		anyOn(cfg.json, cfg.jsonArray, cfg.jsonl, cfg.yaml, cfg.raw, cfg.gron, cfg.gronArray)
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
	writePlanLine(&b, "from", moveFromLabel(cfg))
	if err := writeMovePlanTarget(&b, cfg); err != nil {
		return err
	}
	_, err := fmt.Fprint(cmd.OutOrStdout(), b.String())
	return err
}

// moveFromLabel names the read side of a move plan: stdin, or the --src handle.
func moveFromLabel(cfg *config) string {
	if cfg.src != "" {
		return cfg.src
	}
	return "stdin"
}

// writeMovePlanTarget writes the "to" line and the write section of a move plan.
func writeMovePlanTarget(b *strings.Builder, cfg *config) error {
	if cfg.typed {
		writePlanLine(b, "to", "typed dump (stdout/-o)")
		writePlanSection(b, "write")
		b.WriteString("  encode typed records\n")
		return nil
	}
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	dst, err := resolveEndpoint(cf, cfg.insert, true)
	if err != nil {
		return err
	}
	writePlanLine(b, "to", dst.label())
	writePlanSection(b, "write")
	if d, ok := driverForScheme(schemeOf(dst.url)); ok && d.explainWrite != nil {
		for _, op := range d.explainWrite(writeModeFor(cfg)).Ops {
			b.WriteString("  " + op + "\n")
		}
	}
	return nil
}

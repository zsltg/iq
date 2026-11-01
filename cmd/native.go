package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zsltg/iq/internal/query"
)

// nativeFormatChoices names the renderings that --native can use. The others
// flatten or drop the type tags of the native form.
const nativeFormatChoices = "Choose --json (default), --jsona, --jsonl, or --yaml"

// validateNativeQuery refuses a --native query that cannot honor the flag, before
// any store opens. A filter that calls source() has no single driver to read in
// its native form. A rendering other than JSON or YAML flattens the type tags.
func validateNativeQuery(cfg *config, fm outputFormat, cross bool) error {
	if !cfg.native {
		return nil
	}
	if cross {
		return errors.New("--native cannot read through source(). Run the filter against one source")
	}
	switch fm {
	case formatJSON, formatJSONL, formatJSONArray, formatYAML:
	default:
		return fmt.Errorf("--native cannot use --format %s, because it flattens the type tags. %s", fm, nativeFormatChoices)
	}
	return nil
}

// guardQueryFormat refuses a format that the query cannot use, before any store
// I/O. It refuses a binary format on an interactive terminal, and a --native
// query that cannot honor the flag.
func guardQueryFormat(cmd *cobra.Command, cfg *config, fm outputFormat, cross bool) error {
	if err := guardBinaryFormat(fm, cmd.OutOrStdout()); err != nil {
		return err
	}
	return validateNativeQuery(cfg, fm, cross)
}

// openJQKVStore opens the source of cfg and returns the KVStore that the jq
// engine reads, with the function that closes the source. If the KVStore cannot
// be made, it closes the source and returns the error.
func openJQKVStore(cmd *cobra.Command, ctx context.Context, cfg *config) (query.KVStore, func() error, error) {
	st, err := openJQStore(cmd, ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	kv, err := kvStoreFor(st, cfg)
	if err != nil {
		_ = st.Close()
		return nil, nil, err
	}
	return kv, st.Close, nil
}

// validateNativeMove refuses a --native move that cannot honor the flag. Foreign
// JSON (--key-field) has no native form.
func validateNativeMove(cfg *config) error {
	if cfg.native && cfg.keyField != "" {
		return errors.New("--native cannot read foreign JSON (--key-field), because foreign JSON has no native form")
	}
	return nil
}

// nativeReaderOf returns the native reader of st. The error names the source,
// because most drivers have no native form.
func nativeReaderOf(st any, cfg *config) (query.NativeReader, error) {
	nr, ok := st.(query.NativeReader)
	if !ok {
		return nil, fmt.Errorf("source %s (%s) has no native form. --native works with a DynamoDB source",
			nativeSourceLabel(cfg), driverName(cfg.url))
	}
	return nr, nil
}

// kvStoreFor returns the KVStore that the jq engine reads for st. It is st itself,
// or with --native an adapter over the native scan of st. The adapter has no
// pushdown, so the engine runs the whole filter on the client.
func kvStoreFor(st store, cfg *config) (query.KVStore, error) {
	if !cfg.native {
		return st, nil
	}
	nr, err := nativeReaderOf(st, cfg)
	if err != nil {
		return nil, err
	}
	return query.NewNativeStore(nr, nil), nil
}

// buildQueryPlan renders the query plan of the default action. A --native query
// pushes no predicate down, so the plan shows no filter stages and adds a native
// read section.
func buildQueryPlan(cfg *config, filter string, cross, describe bool) (string, error) {
	if !cfg.native {
		return buildJQPlan(cfg, filter, cross, describe)
	}
	noPush := *cfg
	noPush.noCompile = true
	text, err := buildJQPlan(&noPush, filter, cross, describe)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(text)
	writePlanSection(&b, "native read")
	b.WriteString("  read the native form of the source\n  push no predicate down\n")
	return b.String(), nil
}

// nativeSourceLabel names the source of cfg for an error: its handle, or stdin.
func nativeSourceLabel(cfg *config) string {
	if cfg.handle == "" {
		return "stdin"
	}
	return cfg.handle
}

// checkSourceForm refuses a native source that dst cannot write. It runs before
// --replace empties the destination and before the scan starts, so a refusal
// changes nothing. A source in normalized JSON passes. A piped typed dump has no
// known form before the scan, so only the Copier check covers it.
func checkSourceForm(srcForm query.Form, putter query.Putter, dst endpoint) error {
	own := query.WriterForm(putter)
	if srcForm == query.FormJSON || srcForm == own {
		return nil
	}
	if own == query.FormJSON {
		return fmt.Errorf("--native reads form %q, but destination %s (%s) writes only form %q",
			srcForm, dst.handle, dst.driver, query.FormJSON)
	}
	return fmt.Errorf("--native reads form %q, but destination %s (%s) writes only forms %q and %q",
		srcForm, dst.handle, dst.driver, query.FormJSON, own)
}

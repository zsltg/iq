package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zsltg/iq/internal/shape"
)

// Schema output formats: the contract dialect that `iq schema` projects the
// inferred shape into. The jsonschema format is the default (JSON Schema draft
// 2020-12). The odcs format is an Open Data Contract Standard v3.2.0 contract,
// emitted as YAML (canonical for data contracts). The set is closed, so an
// unknown format fails fast at entry.
const (
	schemaFormatJSONSchema = "jsonschema"
	schemaFormatODCS       = "odcs"
)

// newSchemaCmd builds `iq schema [source]`: sample a source and project the
// inferred shape into a contract format (--format). The positional names the
// source as `inspect` does (empty = --src or the active source, sq-style
// `<source>.<collection>`). The shape is sampled (--sample) and inferred, never
// declared. It is driver-agnostic, the complement to `inspect`, which renders
// the native introspection of each backend. Bounded by --timeout.
func newSchemaCmd(cfg *config) *cobra.Command {
	var (
		sample  int
		yamlOut bool
		format  string
		filter  string
	)
	long := "Sample a source and project a schema inferred from its values.\n\n" +
		"The positional argument names the source, like `iq schema prod`; with none it uses\n" +
		"--src or the active source, and accepts sq-style `<source>.<collection>` addressing.\n" +
		"Unlike `inspect`, which shows a backend's native introspection, `schema` infers a\n" +
		"driver-agnostic shape: the field/type structure sampled from the values themselves.\n" +
		"The shape is sampled (--sample) and inferred, never declared, so a wider sample\n" +
		"yields a truer shape. A `source=<jq>` spec (or --filter) scopes which items the\n" +
		"shape is inferred from, so `iq schema 'prod=.[] | select(.active)'` describes\n" +
		"only the active ones; the sample cap then applies to the survivors, so a\n" +
		"selective filter walks further into the keyspace to fill it.\n\n" +
		"It describes values, not keys; a non-object keyspace is legal\n" +
		"(a string keyspace emits {\"type\":\"string\"}).\n\n" +
		"--format picks the contract dialect the shape projects into:\n" +
		"  jsonschema  JSON Schema draft 2020-12 (default), for interop with code generators,\n" +
		"              iq schema prod.orders > orders.schema.json && quicktype -s schema orders.schema.json -l go\n" +
		"  odcs        Open Data Contract Standard v3.2.0, a YAML data contract for tools such\n" +
		"              as datacontract-cli, Soda, and Great Expectations. It needs objects at the\n" +
		"              root, so a keyspace of other values fails. Use jsonschema for those.\n\n" +
		"jsonschema honors -y to emit YAML instead of JSON; odcs is always YAML (its canonical\n" +
		"form). A schema is field names and types, a few hundred bytes, not a dump of documents."
	c := &cobra.Command{
		Use:               "schema [source[=<jq>]]",
		ValidArgsFunction: completeSourceHandles,
		Short:             "Emit a draft 2020-12 JSON Schema inferred from a sampled source",
		Long:              long,
		Example: "  $ iq schema                      # active source\n" +
			"  $ iq schema shop.orders          # one collection (sq-style handle.collection)\n" +
			"  $ iq schema prod --sample 5000   # widen the sample\n" +
			"  $ iq schema 'prod.orders=.[] | select(.active)'  # shape of a subset\n" +
			"  $ iq schema prod.orders > orders.schema.json  # save for quicktype\n" +
			"  $ iq schema prod.orders --format odcs > orders.odcs.yaml  # emit an ODCS v3.2.0 contract",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateSchemaFormat(format); err != nil {
				return err
			}
			arg := ""
			if len(args) > 0 {
				arg = args[0]
			}
			// The address half selects the source. The filter half (from the "=" of
			// the spec or --filter) scopes which items the shape is inferred from.
			addr, specFilter, _ := strings.Cut(arg, "=")
			if strings.TrimSpace(specFilter) == "" && strings.Contains(arg, "=") {
				return fmt.Errorf("invalid source %q: empty filter", arg)
			}
			if specFilter != "" {
				filter = specFilter
			}
			if err := resolveInspectSource(cfg, addr); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
			defer cancel()
			st, err := openStore(ctx, cfg)
			if err != nil {
				return redactErr(err, cfg.url)
			}
			defer func() { _ = st.Close() }()
			items, err := sampleItems(ctx, st, sampleRequest{filter: filter, sample: sample, opts: cfg.runOptions()})
			if err != nil {
				return fmt.Errorf("sample %q: %w", cfg.handle, redactErr(asSyntaxError(filter, err), cfg.url))
			}
			sh := shape.Infer(items)
			if format == schemaFormatODCS {
				// ODCS is a YAML data contract: emit YAML regardless of -y.
				contract, err := sh.ODCSContract(schemaTitle(cfg))
				if err != nil {
					return fmt.Errorf("cannot write the odcs contract: %w (use --format jsonschema for this source)", err)
				}
				return writeStructured(cmd.OutOrStdout(), contract, true)
			}
			return writeStructured(cmd.OutOrStdout(), sh.JSONSchema(schemaTitle(cfg)), yamlOut)
		},
	}
	c.Flags().IntVar(&sample, "sample", 1000, "max items sampled (0 = all)")
	c.Flags().StringVar(&filter, "filter", "", "jq filter scoping which items the shape is inferred from; the spec form source=<jq> sets it per source")
	c.Flags().BoolVarP(&yamlOut, "yaml", "y", false, "emit YAML instead of JSON (jsonschema only; odcs is always YAML)")
	c.Flags().StringVar(&format, "format", schemaFormatJSONSchema, "contract format: jsonschema or odcs")
	_ = c.RegisterFlagCompletionFunc("format", fixedValues(schemaFormatJSONSchema, schemaFormatODCS))
	return c
}

// validateSchemaFormat rejects an unknown --format at command entry, mirroring
// the fail-fast posture of the output-format flags. The empty string is the
// unset default and resolves to JSON Schema.
func validateSchemaFormat(format string) error {
	switch format {
	case "", schemaFormatJSONSchema, schemaFormatODCS:
		return nil
	default:
		return fmt.Errorf("invalid --format %q: want jsonschema or odcs", format)
	}
}

// schemaTitle names the schema after the resolved source: the handle plus any
// dotted address (shop.orders). It is built from the handle, never the URL, so no
// stored credential can reach the schema title.
func schemaTitle(cfg *config) string {
	if cfg.address != "" {
		return cfg.handle + "." + cfg.address
	}
	return cfg.handle
}

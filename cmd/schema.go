package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zsltg/iq/internal/shape"
)

// Schema output formats: the contract dialect `iq schema` projects the inferred
// shape into. jsonschema is the default (JSON Schema draft 2020-12); odcs is an
// Open Data Contract Standard v3.1.0 contract, emitted as YAML (canonical for
// data contracts). The set is closed so an unknown format fails fast at entry.
const (
	schemaFormatJSONSchema = "jsonschema"
	schemaFormatODCS       = "odcs"
)

// newSchemaCmd builds `iq schema [source]`: sample a source and project the
// inferred shape into a chosen contract format (--format). The positional names
// the source exactly as `inspect` does (empty = --src or the active source,
// sq-style `<source>.<collection>` addressing); the shape is sampled (--sample)
// and inferred, never declared. It renders driver-agnostic sampled inference, the
// complement to `inspect`, which renders each backend's native introspection.
// Bounded by --timeout.
func newSchemaCmd(cfg *config) *cobra.Command {
	var (
		sample  int
		yamlOut bool
		format  string
	)
	long := "Sample a source and project a schema inferred from its values.\n\n" +
		"The positional argument names the source, like `iq schema prod`; with none it uses\n" +
		"--src or the active source, and accepts sq-style `<source>.<collection>` addressing.\n" +
		"Unlike `inspect`, which shows a backend's native introspection, `schema` infers a\n" +
		"driver-agnostic shape: the field/type structure sampled from the values themselves.\n" +
		"The shape is sampled (--sample) and inferred, never declared, so a wider sample\n" +
		"yields a truer shape. It describes values, not keys; a non-object keyspace is legal\n" +
		"(a string keyspace emits {\"type\":\"string\"}).\n\n" +
		"--format picks the contract dialect the shape projects into:\n" +
		"  jsonschema  JSON Schema draft 2020-12 (default), for interop with code generators —\n" +
		"              iq schema prod.orders > orders.schema.json && quicktype -s schema orders.schema.json -l go\n" +
		"  odcs        Open Data Contract Standard v3.1.0, a YAML data contract for tools such\n" +
		"              as datacontract-cli, Soda, and Great Expectations\n\n" +
		"jsonschema honors -y to emit YAML instead of JSON; odcs is always YAML (its canonical\n" +
		"form). A schema is field names and types — a few hundred bytes — not a dump of documents."
	c := &cobra.Command{
		Use:               "schema [source]",
		ValidArgsFunction: completeSourceHandles,
		Short:             "Emit a draft 2020-12 JSON Schema inferred from a sampled source",
		Long:              long,
		Example: "  $ iq schema                      # active source\n" +
			"  $ iq schema shop.orders          # one collection (sq-style handle.collection)\n" +
			"  $ iq schema prod --sample 5000   # widen the sample\n" +
			"  $ iq schema prod.orders > orders.schema.json  # save for quicktype\n" +
			"  $ iq schema prod.orders --format odcs > orders.odcs.yaml  # emit an ODCS v3.1.0 contract",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateSchemaFormat(format); err != nil {
				return err
			}
			arg := ""
			if len(args) > 0 {
				arg = args[0]
			}
			if err := resolveInspectSource(cfg, arg); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
			defer cancel()
			st, err := openStore(ctx, cfg)
			if err != nil {
				return redactErr(err, cfg.url)
			}
			defer func() { _ = st.Close() }()
			items, err := sampleItems(ctx, st, sample)
			if err != nil {
				return fmt.Errorf("sample %q: %w", cfg.handle, redactErr(err, cfg.url))
			}
			sh := shape.Infer(items)
			if format == schemaFormatODCS {
				// ODCS is a YAML data contract: emit YAML regardless of -y.
				return writeStructured(cmd.OutOrStdout(), odcsContract(cfg, sh), true)
			}
			return writeStructured(cmd.OutOrStdout(), sh.JSONSchema(schemaTitle(cfg)), yamlOut)
		},
	}
	c.Flags().IntVar(&sample, "sample", 1000, "max items sampled (0 = all)")
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

// odcsContract wraps a shape's ODCS schema object in an ODCS v3.1.0 fundamentals
// envelope. Every field is deterministic — no timestamps or random ids: the id,
// name, and schema-object name all derive from the resolved source handle and
// keyspace (schemaTitle), and version and status take fixed initial values. The
// handle, never the URL, sources the identifiers, so no stored credential can
// reach the contract.
func odcsContract(cfg *config, sh *shape.Shape) map[string]any {
	name := schemaTitle(cfg)
	return map[string]any{
		"apiVersion": "v3.1.0",
		"kind":       "DataContract",
		"id":         name,
		"name":       name,
		"version":    "1.0.0",
		"status":     "draft",
		"schema":     []any{sh.ODCSSchemaObject(name)},
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

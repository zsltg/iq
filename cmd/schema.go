package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zsltg/iq/internal/shape"
)

// newSchemaCmd builds `iq schema [source]`: sample a source and emit a JSON
// Schema (draft 2020-12) inferred from its values. The positional names the source
// exactly as `inspect` does (empty = --src or the active source, sq-style
// `<source>.<collection>` addressing); the shape is sampled (--sample) and
// inferred, never declared. It renders driver-agnostic sampled inference, the
// complement to `inspect`, which renders each backend's native introspection.
// Bounded by --timeout.
func newSchemaCmd(cfg *config) *cobra.Command {
	var (
		sample  int
		yamlOut bool
	)
	long := "Sample a source and emit a JSON Schema (draft 2020-12) inferred from its values.\n\n" +
		"The positional argument names the source, like `iq schema prod`; with none it uses\n" +
		"--src or the active source, and accepts sq-style `<source>.<collection>` addressing.\n" +
		"Unlike `inspect`, which shows a backend's native introspection, `schema` infers a\n" +
		"driver-agnostic shape: the field/type structure sampled from the values themselves.\n" +
		"The shape is sampled (--sample) and inferred, never declared, so a wider sample\n" +
		"yields a truer shape. It describes values, not keys; a non-object keyspace is legal\n" +
		"(a string keyspace emits {\"type\":\"string\"}).\n\n" +
		"The output is standard JSON Schema, made for interoperation — feed it to a code\n" +
		"generator such as quicktype to derive typed models:\n" +
		"  iq schema prod.orders > orders.schema.json && quicktype -s schema orders.schema.json -l go\n\n" +
		"A schema is field names and types — a few hundred bytes — not a dump of documents."
	c := &cobra.Command{
		Use:   "schema [source]",
		Short: "Emit a draft 2020-12 JSON Schema inferred from a sampled source",
		Long:  long,
		Example: "  $ iq schema                      # active source\n" +
			"  $ iq schema shop.orders          # one collection (sq-style handle.collection)\n" +
			"  $ iq schema prod --sample 5000   # widen the sample\n" +
			"  $ iq schema prod.orders > orders.schema.json  # save for quicktype",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			doc := shape.Infer(items).JSONSchema(schemaTitle(cfg))
			return writeStructured(cmd.OutOrStdout(), doc, yamlOut)
		},
	}
	c.Flags().IntVar(&sample, "sample", 1000, "max items sampled (0 = all)")
	c.Flags().BoolVarP(&yamlOut, "yaml", "y", false, "emit YAML instead of JSON")
	return c
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

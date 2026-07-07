package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	iqcassandra "github.com/zsltg/iq/drivers/cassandra"
	iqcouchdb "github.com/zsltg/iq/drivers/couchdb"
	iqdynamodb "github.com/zsltg/iq/drivers/dynamodb"
	iqelasticsearch "github.com/zsltg/iq/drivers/elasticsearch"
	iqfile "github.com/zsltg/iq/drivers/file"
	iqhbase "github.com/zsltg/iq/drivers/hbase"
	iqmongo "github.com/zsltg/iq/drivers/mongo"
	iqneo4j "github.com/zsltg/iq/drivers/neo4j"
	iqredis "github.com/zsltg/iq/drivers/redis"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// driver describes one backend iq can talk to: a stable name, a human
// description, the URL schemes that select it, its upstream docs, and the opener
// that connects. The drivers registry below is the single source of truth —
// openStore, supportedScheme, driverForScheme, driverName, and `iq driver ls`
// all derive from it, so adding a backend is one entry, not edits scattered
// across a switch. It lives in cmd, the composition root, because open returns
// the cmd-local store port and takes the per-invocation config.
type driver struct {
	name    string
	desc    string
	schemes []string
	doc     string
	// versions is the range of backend server versions the bundled client
	// library supports, shown by `iq driver ls`.
	versions string
	// readOnly marks a local, connection-less backend (a dump file) that iq can
	// only read: it has no write-side describers, is never a copy destination, and
	// has no upstream server to suggest — so doc/versions may be empty for it and
	// it is left out of the connection-scheme hint in error messages.
	readOnly bool
	// formats is the dump-format catalogue a read-only file driver advertises under
	// `iq driver ls -v`; it keeps the summary description short while the format
	// detail stays discoverable. Empty for a live backend, which reads one wire format.
	formats []iqfile.FormatInfo
	// addressable marks a backend whose sources take a dotted address suffix
	// (handle.address) naming a sub-container — MongoDB's collection. A
	// non-addressable backend (Redis, file) rejects an address at resolve time.
	addressable bool
	// verifiesOnOpen marks a backend whose open already round-trips to the server
	// (DynamoDB's connectionless client issues a reachability probe at open), so the
	// post-open health check in `iq ping`/`iq add` needs no second round-trip — like
	// a read-only source, opening it is the reachability check.
	verifiesOnOpen bool
	open           func(ctx context.Context, cfg *config) (store, error)
	// explainPlan describes, without connecting, the backend calls this driver
	// would make for a classified query and pushed predicate — the data the query
	// plan (--explain/--verbose) shows.
	explainPlan func(keys selector.KeySet, pred predicate.Node, unbounded bool) query.AccessPlan
	// explainWrite, explainClear, and explainDrop are the write-side counterparts:
	// each describes an `iq data` operation without connecting, so `--explain`
	// stays connection-free. explainDrop's bool reports whether the backend can
	// drop its container at all (false for Redis, whose DB index is not removable).
	explainWrite func(mode query.WriteMode) query.AccessPlan
	explainClear func() query.AccessPlan
	explainDrop  func() (query.AccessPlan, bool)
}

// drivers is the registry of every backend the CLI can dispatch to. Order is the
// listing order of `iq driver ls` and the enumeration order of expectedSchemes.
var drivers = []driver{
	{
		name:        "mongo",
		desc:        "MongoDB document store",
		schemes:     []string{"mongodb", "mongodb+srv"},
		doc:         "https://www.mongodb.com/docs/",
		versions:    "4.2+",
		addressable: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqmongo.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:  iqmongo.ExplainPlan,
		explainWrite: iqmongo.ExplainWrite,
		explainClear: iqmongo.ExplainClear,
		explainDrop:  iqmongo.ExplainDrop,
	},
	{
		name:        "cassandra",
		desc:        "Apache Cassandra wide-column store",
		schemes:     []string{"cassandra"},
		doc:         "https://cassandra.apache.org/doc/",
		versions:    "3.11+",
		addressable: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqcassandra.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:  iqcassandra.ExplainPlan,
		explainWrite: iqcassandra.ExplainWrite,
		explainClear: iqcassandra.ExplainClear,
		explainDrop:  iqcassandra.ExplainDrop,
	},
	{
		name:           "dynamodb",
		desc:           "Amazon DynamoDB key-value and document store",
		schemes:        []string{"dynamodb"},
		doc:            "https://docs.aws.amazon.com/dynamodb/",
		versions:       "AWS (managed)",
		addressable:    true,
		verifiesOnOpen: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqdynamodb.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:  iqdynamodb.ExplainPlan,
		explainWrite: iqdynamodb.ExplainWrite,
		explainClear: iqdynamodb.ExplainClear,
		explainDrop:  iqdynamodb.ExplainDrop,
	},
	{
		name:           "hbase",
		desc:           "Apache HBase wide-column store",
		schemes:        []string{"hbase"},
		doc:            "https://hbase.apache.org/book.html",
		versions:       "1.0+",
		addressable:    true,
		verifiesOnOpen: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqhbase.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:  iqhbase.ExplainPlan,
		explainWrite: iqhbase.ExplainWrite,
		explainClear: iqhbase.ExplainClear,
		explainDrop:  iqhbase.ExplainDrop,
	},
	{
		name:           "couchdb",
		desc:           "Apache CouchDB document store",
		schemes:        []string{"couchdb", "couchdbs"},
		doc:            "https://docs.couchdb.org/",
		versions:       "2.x, 3.x",
		addressable:    true,
		verifiesOnOpen: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqcouchdb.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:  iqcouchdb.ExplainPlan,
		explainWrite: iqcouchdb.ExplainWrite,
		explainClear: iqcouchdb.ExplainClear,
		explainDrop:  iqcouchdb.ExplainDrop,
	},
	{
		name:           "neo4j",
		desc:           "Neo4j property graph store",
		schemes:        []string{"neo4j", "neo4j+s", "neo4j+ssc", "bolt", "bolt+s", "bolt+ssc"},
		doc:            "https://neo4j.com/docs/",
		versions:       "5.x",
		addressable:    true,
		verifiesOnOpen: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqneo4j.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:  iqneo4j.ExplainPlan,
		explainWrite: iqneo4j.ExplainWrite,
		explainClear: iqneo4j.ExplainClear,
		explainDrop:  iqneo4j.ExplainDrop,
	},
	{
		name:           "elasticsearch",
		desc:           "Elasticsearch search engine and document store",
		schemes:        []string{"elasticsearch", "elasticsearch+s"},
		doc:            "https://www.elastic.co/docs/",
		versions:       "8.x",
		addressable:    true,
		verifiesOnOpen: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqelasticsearch.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:  iqelasticsearch.ExplainPlan,
		explainWrite: iqelasticsearch.ExplainWrite,
		explainClear: iqelasticsearch.ExplainClear,
		explainDrop:  iqelasticsearch.ExplainDrop,
	},
	{
		name:           "opensearch",
		desc:           "OpenSearch search engine and document store",
		schemes:        []string{"opensearch", "opensearch+s"},
		doc:            "https://opensearch.org/docs/",
		versions:       "2.x, 3.x",
		addressable:    true,
		verifiesOnOpen: true,
		// OpenSearch shares the Elasticsearch driver; the source scheme selects the
		// opensearch-go client behind the same query ports.
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqelasticsearch.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:  iqelasticsearch.ExplainPlan,
		explainWrite: iqelasticsearch.ExplainWrite,
		explainClear: iqelasticsearch.ExplainClear,
		explainDrop:  iqelasticsearch.ExplainDrop,
	},
	{
		name:     "redis",
		desc:     "Redis key-value store",
		schemes:  []string{"redis", "rediss"},
		doc:      "https://redis.io/docs/",
		versions: "7.0+",
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqredis.Open(ctx, cfg.url, cfg.trace, cfg.decimalMode)
		},
		explainPlan:  iqredis.ExplainPlan,
		explainWrite: iqredis.ExplainWrite,
		explainClear: iqredis.ExplainClear,
		explainDrop:  iqredis.ExplainDrop,
	},
	{
		name:     "file",
		desc:     "Local dump file, read-only",
		schemes:  []string{"file"},
		readOnly: true,
		formats:  iqfile.SupportedFormats(),
		open: func(_ context.Context, cfg *config) (store, error) {
			return iqfile.Open(cfg.url, cfg.decimalMode, cfg.fileCacheConfig())
		},
		explainPlan: iqfile.ExplainPlan,
	},
}

// driverForScheme returns the driver a URL scheme selects, dispatching over the
// registry. It is the one place that maps a scheme to a backend.
func driverForScheme(scheme string) (driver, bool) {
	for _, d := range drivers {
		for _, s := range d.schemes {
			if s == scheme {
				return d, true
			}
		}
	}
	return driver{}, false
}

// driverName returns the canonical driver name for a connection URL — the stable
// identity shown by `iq driver ls`, `iq ls`, `ping`, `inspect`, and `diff`, so a
// rediss:// and a redis:// source read as the same "redis" driver. An unknown or
// schemeless URL falls back to its raw scheme rather than an empty label.
func driverName(url string) string {
	scheme := schemeOf(url)
	if d, ok := driverForScheme(scheme); ok {
		return d.name
	}
	return scheme
}

// driverByName returns the driver with the given canonical name, dispatching
// over the registry. It backs `iq add -d/--driver`, which names a driver rather
// than a scheme.
func driverByName(name string) (driver, bool) {
	for _, d := range drivers {
		if d.name == name {
			return d, true
		}
	}
	return driver{}, false
}

// driverNames renders the registry's driver names as a comma-separated list for
// an error fragment, so the message stays in sync with the drivers that exist.
func driverNames() string {
	names := make([]string, len(drivers))
	for i, d := range drivers {
		names[i] = d.name
	}
	return strings.Join(names, ", ")
}

// expectedSchemes renders the registry's primary schemes as an error fragment
// ("expected mongodb:// or redis://"), so the message an unsupported URL yields
// stays in sync with the drivers that actually exist.
func expectedSchemes() string {
	// Only connectable backends are suggested for a bad connection URL; a local
	// read-only driver (file://) is discoverable via `iq driver ls`, not here.
	primaries := make([]string, 0, len(drivers))
	for _, d := range drivers {
		if d.readOnly {
			continue
		}
		primaries = append(primaries, d.schemes[0]+"://")
	}
	switch len(primaries) {
	case 1:
		return "expected " + primaries[0]
	case 2:
		return "expected " + primaries[0] + " or " + primaries[1]
	default:
		return "expected one of " + strings.Join(primaries, ", ")
	}
}

// driverRow is the JSON shape of one driver in `iq driver ls --json`. Formats is the
// verbose-only dump-format catalogue, present (with -v) only for a read-only file
// driver and omitted otherwise.
type driverRow struct {
	Driver      string            `json:"driver"`
	Description string            `json:"description"`
	Schemes     []string          `json:"schemes"`
	Versions    string            `json:"versions"`
	Doc         string            `json:"doc"`
	Formats     []driverFormatRow `json:"formats,omitempty"`
}

// driverFormatRow is the JSON shape of one dump format under `iq driver ls -v --json`.
// Auto reports whether content detection recognizes it; when false it must be forced
// with ?format=/--from-format.
type driverFormatRow struct {
	Name   string `json:"name"`
	Auto   bool   `json:"auto"`
	Source string `json:"source"`
}

// newDriverCmd builds `iq driver`: the backend-registry command group. Its `ls`
// subcommand lists the drivers iq can dispatch to, mirroring sq's `driver ls`.
func newDriverCmd(cfg *config) *cobra.Command {
	c := &cobra.Command{
		Use:     "driver",
		Short:   "Inspect the backends iq can talk to",
		Example: "  $ iq driver ls # list the backend drivers iq can dispatch to",
	}
	c.AddCommand(newDriverLsCmd(cfg))
	return c
}

// newDriverLsCmd builds `iq driver ls`: list the registered backend drivers, each
// with its description, the URL schemes that select it, and its upstream docs. The
// global -v/--verbose appends the read-only file driver's dump-format catalogue.
func newDriverLsCmd(cfg *config) *cobra.Command {
	var jsonOut, yamlOut bool
	c := &cobra.Command{
		Use:   "ls",
		Short: "List the backend drivers iq can dispatch to",
		Long: "List the backend drivers iq can dispatch to. Each row shows the driver's stable\n" +
			"name (as `iq ls` reports it), a description, the URL schemes that select it, the\n" +
			"backend server versions the bundled client library supports, and a link to its\n" +
			"upstream documentation. -v appends the file driver's readable dump formats (which\n" +
			"auto-detect, which need ?format=). -j/--json or -y/--yaml emit machine-readable output.",
		Example: "  $ iq driver ls    # list the backend drivers\n" +
			"  $ iq driver ls -v # also list the file driver's dump formats",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return listDrivers(cmd.OutOrStdout(), cfg.verbose, jsonOut, yamlOut)
		},
	}
	c.Flags().BoolVarP(&jsonOut, "json", "j", false, "emit machine-readable JSON")
	c.Flags().BoolVarP(&yamlOut, "yaml", "y", false, "emit machine-readable YAML")
	c.MarkFlagsMutuallyExclusive("json", "yaml")
	return c
}

// listDrivers renders the driver registry as an aligned table (with a header row) or,
// with jsonOut/yamlOut set, as machine-readable output. verbose appends each
// format-bearing driver's dump-format catalogue (the file driver's readers).
func listDrivers(out io.Writer, verbose, jsonOut, yamlOut bool) error {
	if jsonOut || yamlOut {
		rows := make([]driverRow, 0, len(drivers))
		for _, d := range drivers {
			row := driverRow{
				Driver:      d.name,
				Description: d.desc,
				Schemes:     d.schemes,
				Versions:    d.versions,
				Doc:         d.doc,
			}
			if verbose {
				for _, fi := range d.formats {
					row.Formats = append(row.Formats, driverFormatRow{Name: fi.Name(), Auto: fi.Auto, Source: fi.Source})
				}
			}
			rows = append(rows, row)
		}
		return writeStructured(out, rows, yamlOut)
	}
	rows := [][]tableCell{{
		coloredCell("DRIVER", pal.header),
		coloredCell("DESCRIPTION", pal.header),
		coloredCell("SCHEMES", pal.header),
		coloredCell("VERSIONS", pal.header),
		coloredCell("DOC", pal.header),
	}}
	for _, d := range drivers {
		rows = append(rows, []tableCell{
			cell(d.name),
			cell(d.desc),
			cell(strings.Join(d.schemes, ", ")),
			cell(d.versions),
			cell(d.doc),
		})
	}
	if err := renderTable(out, rows); err != nil {
		return err
	}
	if verbose {
		for _, d := range drivers {
			if err := writeDriverFormats(out, d); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeDriverFormats appends a driver's dump-format catalogue under `iq driver ls -v`:
// a caption then an aligned FORMAT/marker/SOURCE block, rendered as its own table so
// the main grid's column widths are untouched. A driver without formats writes nothing.
func writeDriverFormats(out io.Writer, d driver) error {
	if len(d.formats) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(out, "\n%s dump formats (auto-detected unless ?format= shown):\n", d.name); err != nil {
		return err
	}
	rows := make([][]tableCell, 0, len(d.formats))
	for _, fi := range d.formats {
		mark := ""
		if !fi.Auto {
			mark = "?format="
		}
		rows = append(rows, []tableCell{
			coloredCell("  "+fi.Name(), pal.change),
			coloredCell(mark, pal.faint),
			coloredCell(fi.Source, pal.faint),
		})
	}
	return renderTable(out, rows)
}

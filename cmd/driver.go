package cmd

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	iqcassandra "github.com/zsltg/iq/drivers/cassandra"
	iqcouchbase "github.com/zsltg/iq/drivers/couchbase"
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
	// addressParams names the URL query params that pin a source's default
	// keyspace, most specific first (Couchbase's ?collection= outranks its
	// ?bucket=). `iq add` derives a handle from the first one the URL sets, and
	// urlAddressUnsupported rejects any of these spellings on a backend that
	// declares none. Empty for a backend with no keyspace (Redis, file), which is
	// exactly the set that is not addressable.
	addressParams []string
	// urlParams names query params a keyspace-less backend reads for its own
	// purposes, so the foreign-keyspace guard does not mistake one for a keyspace
	// it cannot honour — the file driver takes ?label= and ?rel= as decode hints
	// for a graph dump. Empty for a backend that declares addressParams, which
	// owns its whole query string.
	urlParams []string
	// verifiesOnOpen marks a backend whose open already round-trips to the server
	// (DynamoDB's connectionless client issues a reachability probe at open), so the
	// post-open health check in `iq ping`/`iq add` needs no second round-trip — like
	// a read-only source, opening it is the reachability check.
	verifiesOnOpen bool
	// filtersScan marks a driver that narrows a scan with a pushed predicate —
	// server-side, or a client-side raw-byte prefilter (Redis) — i.e. one that
	// implements the FilteredScanner capability. It mirrors that capability so
	// `--explain` can tell a conjunct a backend declined from a driver that never
	// filters a scan at all (the read-only file driver), reporting each conjunct as
	// client-side with the right reason. A driver that leaves it false is scanned
	// whole and filtered client-side.
	filtersScan bool
	open        func(ctx context.Context, cfg *config) (store, error)
	// explainPlan describes, without connecting, the backend calls this driver
	// would make for a classified query and pushed predicate — the data the query
	// plan (--explain/--verbose) shows.
	explainPlan func(keys selector.KeySet, pred predicate.Node, unbounded bool) query.AccessPlan
	// explainWrite, explainClear, explainDrop, and explainDelete are the write-side
	// counterparts: each describes an `iq data` operation without connecting, so
	// `--explain` stays connection-free. explainDrop's and explainDelete's bool
	// reports whether the backend supports the operation at all (drop is false for
	// Redis, whose DB index is not removable; delete is nil for a backend with no
	// per-key identity, such as the read-only file driver).
	explainWrite  func(mode query.WriteMode) query.AccessPlan
	explainClear  func() query.AccessPlan
	explainDrop   func() (query.AccessPlan, bool)
	explainDelete func() (query.AccessPlan, bool)
}

// drivers is the registry of every backend the CLI can dispatch to. Order is the
// listing order of `iq driver ls` and the enumeration order of expectedSchemes.
var drivers = []driver{
	{
		name:          "mongo",
		desc:          "MongoDB document store",
		schemes:       []string{"mongodb", "mongodb+srv"},
		doc:           "https://www.mongodb.com/docs/",
		versions:      "4.2+",
		addressable:   true,
		addressParams: []string{"collection"},
		filtersScan:   true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqmongo.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   iqmongo.ExplainPlan,
		explainWrite:  iqmongo.ExplainWrite,
		explainClear:  iqmongo.ExplainClear,
		explainDrop:   iqmongo.ExplainDrop,
		explainDelete: iqmongo.ExplainDelete,
	},
	{
		name:          "cassandra",
		desc:          "Apache Cassandra wide-column store",
		schemes:       []string{"cassandra"},
		doc:           "https://cassandra.apache.org/doc/",
		versions:      "3.11+",
		addressable:   true,
		addressParams: []string{"table"},
		filtersScan:   true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqcassandra.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   iqcassandra.ExplainPlan,
		explainWrite:  iqcassandra.ExplainWrite,
		explainClear:  iqcassandra.ExplainClear,
		explainDrop:   iqcassandra.ExplainDrop,
		explainDelete: iqcassandra.ExplainDelete,
	},
	{
		name:           "dynamodb",
		desc:           "Amazon DynamoDB key-value and document store",
		schemes:        []string{"dynamodb"},
		doc:            "https://docs.aws.amazon.com/dynamodb/",
		versions:       "AWS (managed)",
		addressable:    true,
		addressParams:  []string{"table"},
		verifiesOnOpen: true,
		filtersScan:    true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqdynamodb.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   iqdynamodb.ExplainPlan,
		explainWrite:  iqdynamodb.ExplainWrite,
		explainClear:  iqdynamodb.ExplainClear,
		explainDrop:   iqdynamodb.ExplainDrop,
		explainDelete: iqdynamodb.ExplainDelete,
	},
	{
		name:           "hbase",
		desc:           "Apache HBase wide-column store",
		schemes:        []string{"hbase"},
		doc:            "https://hbase.apache.org/book.html",
		versions:       "1.0+",
		addressable:    true,
		addressParams:  []string{"table"},
		verifiesOnOpen: true,
		filtersScan:    true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqhbase.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   iqhbase.ExplainPlan,
		explainWrite:  iqhbase.ExplainWrite,
		explainClear:  iqhbase.ExplainClear,
		explainDrop:   iqhbase.ExplainDrop,
		explainDelete: iqhbase.ExplainDelete,
	},
	{
		name:           "couchdb",
		desc:           "Apache CouchDB document store",
		schemes:        []string{"couchdb", "couchdbs"},
		doc:            "https://docs.couchdb.org/",
		versions:       "2.x, 3.x",
		addressable:    true,
		addressParams:  []string{"database"},
		verifiesOnOpen: true,
		filtersScan:    true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqcouchdb.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   iqcouchdb.ExplainPlan,
		explainWrite:  iqcouchdb.ExplainWrite,
		explainClear:  iqcouchdb.ExplainClear,
		explainDrop:   iqcouchdb.ExplainDrop,
		explainDelete: iqcouchdb.ExplainDelete,
	},
	{
		name:           "couchbase",
		desc:           "Couchbase document store",
		schemes:        []string{"couchbase", "couchbases"},
		doc:            "https://docs.couchbase.com/",
		versions:       "7.x, 8.x (Community or Enterprise)",
		addressable:    true,
		addressParams:  []string{"collection", "bucket"},
		verifiesOnOpen: true,
		filtersScan:    true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqcouchbase.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:  iqcouchbase.ExplainPlan,
		explainWrite: iqcouchbase.ExplainWrite,
		explainClear: iqcouchbase.ExplainClear,
		explainDrop:  iqcouchbase.ExplainDrop,
	},
	{
		name:           "neo4j",
		desc:           "Neo4j property graph store",
		schemes:        []string{"neo4j", "neo4j+s", "neo4j+ssc", "bolt", "bolt+s", "bolt+ssc"},
		doc:            "https://neo4j.com/docs/",
		versions:       "5.x",
		addressable:    true,
		addressParams:  []string{"label", "rel", "database"},
		verifiesOnOpen: true,
		filtersScan:    true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqneo4j.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   iqneo4j.ExplainPlan,
		explainWrite:  iqneo4j.ExplainWrite,
		explainClear:  iqneo4j.ExplainClear,
		explainDrop:   iqneo4j.ExplainDrop,
		explainDelete: iqneo4j.ExplainDelete,
	},
	{
		name:           "elasticsearch",
		desc:           "Elasticsearch search engine and document store",
		schemes:        []string{"elasticsearch", "elasticsearch+s"},
		doc:            "https://www.elastic.co/docs/",
		versions:       "8.x",
		addressable:    true,
		addressParams:  []string{"index"},
		verifiesOnOpen: true,
		filtersScan:    true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqelasticsearch.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   iqelasticsearch.ExplainPlan,
		explainWrite:  iqelasticsearch.ExplainWrite,
		explainClear:  iqelasticsearch.ExplainClear,
		explainDrop:   iqelasticsearch.ExplainDrop,
		explainDelete: iqelasticsearch.ExplainDelete,
	},
	{
		name:           "opensearch",
		desc:           "OpenSearch search engine and document store",
		schemes:        []string{"opensearch", "opensearch+s"},
		doc:            "https://opensearch.org/docs/",
		versions:       "2.x, 3.x",
		addressable:    true,
		addressParams:  []string{"index"},
		verifiesOnOpen: true,
		// OpenSearch shares the Elasticsearch driver; the source scheme selects the
		// opensearch-go client behind the same query ports.
		filtersScan: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqelasticsearch.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   iqelasticsearch.ExplainPlan,
		explainWrite:  iqelasticsearch.ExplainWrite,
		explainClear:  iqelasticsearch.ExplainClear,
		explainDrop:   iqelasticsearch.ExplainDrop,
		explainDelete: iqelasticsearch.ExplainDelete,
	},
	{
		name:        "redis",
		desc:        "Redis key-value store",
		schemes:     []string{"redis", "rediss"},
		doc:         "https://redis.io/docs/",
		versions:    "7.0+",
		filtersScan: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqredis.Open(ctx, cfg.url, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   iqredis.ExplainPlan,
		explainWrite:  iqredis.ExplainWrite,
		explainClear:  iqredis.ExplainClear,
		explainDrop:   iqredis.ExplainDrop,
		explainDelete: iqredis.ExplainDelete,
	},
	{
		name:      "file",
		desc:      "Local dump file, read-only",
		schemes:   []string{"file"},
		readOnly:  true,
		urlParams: []string{"label", "rel"},
		formats:   iqfile.SupportedFormats(),
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

// urlAddressName returns the keyspace a source URL pins through a driver-owned
// query param: the value of the first of the driver's addressParams the URL sets,
// most specific first. It is what `iq add` names a source after when -n is
// omitted. Empty when the driver declares no keyspace param, the URL sets none,
// or the URL does not parse.
func urlAddressName(rawURL string) string {
	d, ok := driverForScheme(schemeOf(rawURL))
	if !ok {
		return ""
	}
	q, ok := urlQuery(rawURL)
	if !ok {
		return ""
	}
	for _, p := range d.addressParams {
		if v := q.Get(p); v != "" {
			return v
		}
	}
	return ""
}

// foreignAddressParam returns the name of a keyspace param a URL sets that its
// backend cannot honour: any spelling in the registry's union on a backend that
// declares none (Redis, file), minus the params that backend reads itself. It is
// the guard `iq add` runs so a mistaken default fails fast rather than being
// silently ignored at connect time. Empty when the URL carries no such param.
func foreignAddressParam(rawURL string) string {
	d, ok := driverForScheme(schemeOf(rawURL))
	if !ok || len(d.addressParams) > 0 {
		return ""
	}
	q, ok := urlQuery(rawURL)
	if !ok {
		return ""
	}
	for _, p := range allAddressParams() {
		if slices.Contains(d.urlParams, p) {
			continue
		}
		if q.Get(p) != "" {
			return p
		}
	}
	return ""
}

// allAddressParams returns every keyspace param name in the registry, deduped and
// in registry order, so the guard's report is deterministic and a new backend's
// spelling joins it without a second edit.
func allAddressParams() []string {
	params := make([]string, 0, len(drivers))
	for _, d := range drivers {
		for _, p := range d.addressParams {
			if !slices.Contains(params, p) {
				params = append(params, p)
			}
		}
	}
	return params
}

// urlQuery parses a source URL's query string. An unparseable URL yields false
// rather than an error: every caller treats it as "names nothing", leaving the
// driver to report the real parse failure at open time.
func urlQuery(rawURL string) (url.Values, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, false
	}
	return u.Query(), true
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

// driverNameList returns the registry's canonical driver names in registry
// order. It backs both the driverNames error fragment and `iq add -d`'s shell
// completion, so neither can drift from the drivers that exist.
func driverNameList() []string {
	names := make([]string, len(drivers))
	for i, d := range drivers {
		names[i] = d.name
	}
	return names
}

// driverNames renders the registry's driver names as a comma-separated list for
// an error fragment, so the message stays in sync with the drivers that exist.
func driverNames() string {
	return strings.Join(driverNameList(), ", ")
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
// a caption teaching the URL shape, then an aligned name/source block rendered as its
// own table so the main grid's column widths are untouched. A content-detected format
// shows a bare name (usable as file:///<path>); one that must be forced shows the full
// ?format=<name> to paste onto file:///<path>?format=<name>. Nothing for a driver
// without formats.
func writeDriverFormats(out io.Writer, d driver) error {
	if len(d.formats) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(out, "\n%s dump formats — a bare name auto-detects "+
		"(file:///<file_path>), the ?format= form must be passed "+
		"(file:///<file_path>?format=<source_format>):\n", d.name); err != nil {
		return err
	}
	rows := make([][]tableCell, 0, len(d.formats))
	for _, fi := range d.formats {
		name := fi.Name()
		if !fi.Auto {
			name = "?format=" + name
		}
		rows = append(rows, []tableCell{
			coloredCell("  "+name, pal.change),
			coloredCell(fi.Source, pal.faint),
		})
	}
	return renderTable(out, rows)
}

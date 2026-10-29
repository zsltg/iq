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

// driver describes one backend that iq can talk to. It holds a stable name, a
// description, the URI schemes that select it, its upstream docs, and the
// opener. The drivers registry below is the single source of truth.
// openStore, supportedScheme, driverForScheme, driverName, and `iq driver ls`
// all derive from it. So a new backend is one entry. It lives in cmd, the
// composition root. The reason is that open returns the cmd-local store port
// and takes the per-invocation config.
type driver struct {
	name    string
	desc    string
	schemes []string
	doc     string
	// versions is the range of backend server versions the bundled client
	// library supports, shown by `iq driver ls`.
	versions string
	// readOnly marks a local, connection-less backend (a dump file) that iq can
	// only read. It has no write-side describers, is never a copy destination,
	// and has no upstream server to suggest. So doc and versions can be empty,
	// and the connection-scheme hint in error messages leaves it out.
	readOnly bool
	// formats is the dump-format catalogue that a read-only file driver advertises
	// under `iq driver ls -v`. It keeps the summary description short while the
	// format detail stays discoverable. Empty for a live backend, which reads one
	// wire format.
	formats []iqfile.FormatInfo
	// addressable marks a backend whose sources take a dotted address suffix
	// (handle.address) that names a sub-container, such as the collection of
	// MongoDB. A non-addressable backend (Redis, file) rejects an address at
	// resolve time.
	addressable bool
	// params is the catalogue of URI query options that iq itself reads for this
	// backend. The driver package owns it, so its parse code and this registry
	// share one list. Options that a backend SDK parses are not listed. The
	// keyspace options pin the default keyspace of a source: see addressParams.
	// They are in catalogue order (most specific first, so ?collection= of
	// Couchbase outranks its ?bucket=).
	params []query.URIParam
	// uriExample is a sample URI for the `iq add` help. uriNote says what the host
	// part names, when the host is not a plain server.
	uriExample string
	uriNote    string
	// shape says how a URI of this backend names its server and container. The
	// replace guard of `--insert` reads it to compare two endpoints.
	shape endpointShape
	// verifiesOnOpen marks a backend whose open already round-trips to the
	// server (the connectionless DynamoDB client issues a reachability probe at
	// open). The post-open health check of `iq ping` and `iq add` then needs no
	// second round-trip.
	verifiesOnOpen bool
	open           func(ctx context.Context, cfg *config) (store, error)
	// explainPlan describes, without connecting, the backend calls that this
	// driver makes if it runs a classified query and pushed predicate. That is
	// the data that the query plan (--explain/--verbose) shows. Only a driver
	// whose plan depends on the URI reads it, and only that driver returns an
	// error.
	explainPlan func(src planSource, keys selector.KeySet, pred predicate.Node, unbounded bool) (query.AccessPlan, error)
	// explainWrite, explainClear, explainDrop, and explainDelete are the
	// write-side counterparts. Each describes an `iq data` operation without
	// connecting, so `--explain` stays connection-free. The bool of explainDrop
	// and explainDelete reports whether the backend supports the operation at
	// all. Drop is false for Redis, whose DB index is not removable. Delete is
	// nil for a backend with no per-key identity, such as the read-only file
	// driver.
	explainWrite  func(mode query.WriteMode) query.AccessPlan
	explainClear  func() query.AccessPlan
	explainDrop   func() (query.AccessPlan, bool)
	explainDelete func() (query.AccessPlan, bool)
}

// planSource is the source a plan describes: the saved URI and the resolved address.
// Execution opens the store with the same two values, so planning parses what the
// open parses.
type planSource struct {
	url     string
	address string
}

// planSource returns the plan source of the config.
func (cfg *config) planSource() planSource {
	return planSource{url: cfg.url, address: cfg.address}
}

// planSource returns the plan source of the endpoint.
func (e endpoint) planSource() planSource {
	return planSource{url: e.url, address: e.address}
}

// withoutURI adapts a plan function that ignores the URI and cannot fail to the
// registry signature.
func withoutURI(f func(keys selector.KeySet, pred predicate.Node, unbounded bool) query.AccessPlan) func(planSource, selector.KeySet, predicate.Node, bool) (query.AccessPlan, error) {
	return func(_ planSource, keys selector.KeySet, pred predicate.Node, unbounded bool) (query.AccessPlan, error) {
		return f(keys, pred, unbounded), nil
	}
}

// drivers is the registry of every backend the CLI can dispatch to. Order is the
// listing order of `iq driver ls` and the enumeration order of expectedSchemes.
var drivers = []driver{
	{
		name: "mongo",
		// 27017 mirrors the default port of the MongoDB Go driver.
		shape:       endpointShape{defaultPort: "27017", path: pathScope},
		desc:        "MongoDB document store",
		schemes:     []string{"mongodb", "mongodb+srv"},
		doc:         "https://www.mongodb.com/docs/",
		versions:    "4.2+",
		addressable: true,
		params:      iqmongo.URIParams,
		uriExample:  "mongodb://host/db?collection=orders",
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqmongo.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   withoutURI(iqmongo.ExplainPlan),
		explainWrite:  iqmongo.ExplainWrite,
		explainClear:  iqmongo.ExplainClear,
		explainDrop:   iqmongo.ExplainDrop,
		explainDelete: iqmongo.ExplainDelete,
	},
	{
		name:        "cassandra",
		shape:       endpointShape{defaultPort: iqcassandra.DefaultPort, path: pathScope},
		desc:        "Apache Cassandra wide-column store",
		schemes:     []string{"cassandra"},
		doc:         "https://cassandra.apache.org/doc/",
		versions:    "3.11+",
		addressable: true,
		params:      iqcassandra.URIParams,
		uriExample:  "cassandra://host/keyspace?table=orders",
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqcassandra.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   withoutURI(iqcassandra.ExplainPlan),
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
		params:         iqdynamodb.URIParams,
		uriExample:     "dynamodb://us-east-1/?table=orders",
		uriNote:        "The host is the region. Credentials come from the AWS default chain.",
		verifiesOnOpen: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqdynamodb.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   withoutURI(iqdynamodb.ExplainPlan),
		explainWrite:  iqdynamodb.ExplainWrite,
		explainClear:  iqdynamodb.ExplainClear,
		explainDrop:   iqdynamodb.ExplainDrop,
		explainDelete: iqdynamodb.ExplainDelete,
	},
	{
		name:           "hbase",
		shape:          endpointShape{defaultPort: iqhbase.DefaultZKPort},
		desc:           "Apache HBase wide-column store",
		schemes:        []string{"hbase"},
		doc:            "https://hbase.apache.org/book.html",
		versions:       "1.0+",
		addressable:    true,
		params:         iqhbase.URIParams,
		uriExample:     "hbase://host:2181/?table=books",
		uriNote:        "The host is the ZooKeeper quorum.",
		verifiesOnOpen: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqhbase.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan: func(src planSource, keys selector.KeySet, pred predicate.Node, unbounded bool) (query.AccessPlan, error) {
			return iqhbase.ExplainPlan(iqhbase.Source{URI: src.url, Address: src.address}, keys, pred, unbounded)
		},
		explainWrite:  iqhbase.ExplainWrite,
		explainClear:  iqhbase.ExplainClear,
		explainDrop:   iqhbase.ExplainDrop,
		explainDelete: iqhbase.ExplainDelete,
	},
	{
		name: "couchdb",
		// The driver hands the host to an HTTP client. A missing port means 80,
		// or 443 for the TLS scheme.
		shape:          endpointShape{defaultPort: "80", tlsPort: "443", tlsSchemes: []string{"couchdbs"}, path: pathAddress},
		desc:           "Apache CouchDB document store",
		schemes:        []string{"couchdb", "couchdbs"},
		doc:            "https://docs.couchdb.org/",
		versions:       "2.x, 3.x",
		addressable:    true,
		params:         iqcouchdb.URIParams,
		uriExample:     "couchdb://host:5984/?database=orders",
		verifiesOnOpen: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqcouchdb.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   withoutURI(iqcouchdb.ExplainPlan),
		explainWrite:  iqcouchdb.ExplainWrite,
		explainClear:  iqcouchdb.ExplainClear,
		explainDrop:   iqcouchdb.ExplainDrop,
		explainDelete: iqcouchdb.ExplainDelete,
	},
	{
		name:           "couchbase",
		shape:          endpointShape{anyPort: true},
		desc:           "Couchbase document store",
		schemes:        []string{"couchbase", "couchbases"},
		doc:            "https://docs.couchbase.com/",
		versions:       "7.x, 8.x (Community or Enterprise)",
		addressable:    true,
		params:         iqcouchbase.URIParams,
		uriExample:     "couchbase://host/?bucket=iq&collection=sales.orders",
		verifiesOnOpen: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqcouchbase.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:  withoutURI(iqcouchbase.ExplainPlan),
		explainWrite: iqcouchbase.ExplainWrite,
		explainClear: iqcouchbase.ExplainClear,
		explainDrop:  iqcouchbase.ExplainDrop,
	},
	{
		name: "neo4j",
		// 7687 mirrors the default Bolt port of the Neo4j Go driver.
		shape:          endpointShape{defaultPort: "7687"},
		desc:           "Neo4j property graph store",
		schemes:        []string{"neo4j", "neo4j+s", "neo4j+ssc", "bolt", "bolt+s", "bolt+ssc"},
		doc:            "https://neo4j.com/docs/",
		versions:       "5.x",
		addressable:    true,
		params:         iqneo4j.URIParams,
		uriExample:     "neo4j://host:7687/?label=Person",
		uriNote:        "The host is the Bolt server.",
		verifiesOnOpen: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqneo4j.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   withoutURI(iqneo4j.ExplainPlan),
		explainWrite:  iqneo4j.ExplainWrite,
		explainClear:  iqneo4j.ExplainClear,
		explainDrop:   iqneo4j.ExplainDrop,
		explainDelete: iqneo4j.ExplainDelete,
	},
	{
		name: "elasticsearch",
		// The driver hands the host to an HTTP client. A missing port means 80,
		// or 443 for the TLS scheme.
		shape:          endpointShape{defaultPort: "80", tlsPort: "443", tlsSchemes: []string{"elasticsearch+s"}, path: pathAddress},
		desc:           "Elasticsearch search engine and document store",
		schemes:        []string{"elasticsearch", "elasticsearch+s"},
		doc:            "https://www.elastic.co/docs/",
		versions:       "8.x, 9.x",
		addressable:    true,
		params:         iqelasticsearch.URIParams,
		uriExample:     "elasticsearch://host:9200/?index=books",
		verifiesOnOpen: true,
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqelasticsearch.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   withoutURI(iqelasticsearch.ExplainPlan),
		explainWrite:  iqelasticsearch.ExplainWrite,
		explainClear:  iqelasticsearch.ExplainClear,
		explainDrop:   iqelasticsearch.ExplainDrop,
		explainDelete: iqelasticsearch.ExplainDelete,
	},
	{
		name: "opensearch",
		// The driver hands the host to an HTTP client. A missing port means 80,
		// or 443 for the TLS scheme.
		shape:          endpointShape{defaultPort: "80", tlsPort: "443", tlsSchemes: []string{"opensearch+s"}, path: pathAddress},
		desc:           "OpenSearch search engine and document store",
		schemes:        []string{"opensearch", "opensearch+s"},
		doc:            "https://opensearch.org/docs/",
		versions:       "2.x, 3.x",
		addressable:    true,
		params:         iqelasticsearch.URIParams,
		uriExample:     "opensearch://host:9200/?index=books",
		verifiesOnOpen: true,
		// OpenSearch shares the Elasticsearch driver. The source scheme selects the
		// opensearch-go client behind the same query ports.
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqelasticsearch.Open(ctx, cfg.url, cfg.address, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   withoutURI(iqelasticsearch.ExplainPlan),
		explainWrite:  iqelasticsearch.ExplainWrite,
		explainClear:  iqelasticsearch.ExplainClear,
		explainDrop:   iqelasticsearch.ExplainDrop,
		explainDelete: iqelasticsearch.ExplainDelete,
	},
	{
		name: "redis",
		// 6379 mirrors the default port of go-redis.
		shape:    endpointShape{defaultPort: "6379", path: pathDBIndex},
		desc:     "Redis key-value store",
		schemes:  []string{"redis", "rediss"},
		doc:      "https://redis.io/docs/",
		versions: "7.0+",
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqredis.Open(ctx, cfg.url, cfg.trace, cfg.decimalMode)
		},
		explainPlan:   withoutURI(iqredis.ExplainPlan),
		explainWrite:  iqredis.ExplainWrite,
		explainClear:  iqredis.ExplainClear,
		explainDrop:   iqredis.ExplainDrop,
		explainDelete: iqredis.ExplainDelete,
	},
	{
		name:       "file",
		desc:       "Local dump file, read-only",
		schemes:    []string{"file"},
		readOnly:   true,
		params:     iqfile.URIParams,
		uriExample: "file:///dumps/orders.bson?format=bson",
		formats:    iqfile.SupportedFormats(),
		open: func(_ context.Context, cfg *config) (store, error) {
			return iqfile.Open(cfg.url, cfg.decimalMode, cfg.fileCacheConfig())
		},
		explainPlan: withoutURI(iqfile.ExplainPlan),
	},
}

// addressParams names the URI query params that pin a source's default keyspace,
// most specific first (Couchbase's ?collection= outranks its ?bucket=). `iq add`
// derives a handle from the first one the URI sets, and urlAddressUnsupported
// rejects any of these spellings on a backend that declares none. Empty for a
// backend with no keyspace (Redis, file), which is exactly the set that is not
// addressable.
func (d driver) addressParams() []string {
	var names []string
	for _, p := range d.params {
		if p.Keyspace {
			names = append(names, p.Name)
		}
	}
	return names
}

// ownParams names the non-keyspace query params that a backend reads itself, so
// the foreign-keyspace guard does not mistake one for a keyspace it cannot
// honour. The file driver takes ?label= and ?rel= as decode hints for a graph
// dump.
func (d driver) ownParams() []string {
	var names []string
	for _, p := range d.params {
		if !p.Keyspace {
			names = append(names, p.Name)
		}
	}
	return names
}

// driverForScheme returns the driver a URL scheme selects, dispatching over the
// registry. It is the one place that maps a scheme to a backend.
func driverForScheme(scheme string) (driver, bool) {
	for _, d := range drivers {
		if slices.Contains(d.schemes, scheme) {
			return d, true
		}
	}
	return driver{}, false
}

// driverName returns the canonical driver name for a connection URL. It is the
// stable identity shown by `iq driver ls`, `iq ls`, `ping`, `inspect`, and
// `diff`. So a rediss:// and a redis:// source read as the same "redis" driver.
// An unknown or schemeless URL falls back to its raw scheme and not an empty
// label.
func driverName(url string) string {
	scheme := schemeOf(url)
	if d, ok := driverForScheme(scheme); ok {
		return d.name
	}
	return scheme
}

// urlAddressName returns the keyspace that a source URL pins through a driver-owned
// query param. That is the value of the first param that the URL sets, from the
// addressParams() of the driver, most specific first. It is what `iq add` names a
// source after when -n is omitted. Empty when the driver declares no keyspace
// param, when the URL sets none, or when the URL does not parse.
func urlAddressName(rawURL string) string {
	d, ok := driverForScheme(schemeOf(rawURL))
	if !ok {
		return ""
	}
	q, ok := urlQuery(rawURL)
	if !ok {
		return ""
	}
	for _, p := range d.addressParams() {
		if v := q.Get(p); v != "" {
			return v
		}
	}
	return ""
}

// foreignAddressParam returns the name of a keyspace param that a URL sets and
// that its backend cannot honor. That is any spelling in the union of the
// registry on a backend that declares none (Redis, file), minus the params that
// backend reads itself. It is the guard that `iq add` runs, so a mistaken
// default fails fast and is not silently ignored at connect time. Empty when
// the URL carries no such param.
func foreignAddressParam(rawURL string) string {
	d, ok := driverForScheme(schemeOf(rawURL))
	if !ok || len(d.addressParams()) > 0 {
		return ""
	}
	q, ok := urlQuery(rawURL)
	if !ok {
		return ""
	}
	own := d.ownParams()
	for _, p := range allAddressParams() {
		if slices.Contains(own, p) {
			continue
		}
		if q.Get(p) != "" {
			return p
		}
	}
	return ""
}

// allAddressParams returns every keyspace param name in the registry, deduped and
// in registry order. So the report of the guard is deterministic, and the
// spelling of a new backend joins it without a second edit.
func allAddressParams() []string {
	params := make([]string, 0, len(drivers))
	for _, d := range drivers {
		for _, p := range d.addressParams() {
			if !slices.Contains(params, p) {
				params = append(params, p)
			}
		}
	}
	return params
}

// uriOptionsHelp renders the per-driver URI options of `iq add` from the driver
// catalogues, so the help cannot drift from the options the drivers read. Each
// driver is one wrapped paragraph, which the man page and the markdown docs keep.
func uriOptionsHelp() string {
	var b strings.Builder
	b.WriteString("Each driver reads its own URI options. A keyspace option sets the default\n" +
		"collection, table, or index of the source. When a URI sets more than one\n" +
		"keyspace option, the first one in the list names the source.")
	for _, d := range drivers {
		if len(d.params) == 0 {
			continue
		}
		b.WriteString("\n\n" + wrapText(driverOptionsHelp(d)))
	}
	return b.String()
}

// driverOptionsHelp renders one driver as a paragraph: the example URI, the
// note, then one sentence for each option.
func driverOptionsHelp(d driver) string {
	parts := []string{d.name + ": " + d.uriExample + "."}
	if d.uriNote != "" {
		parts = append(parts, d.uriNote)
	}
	for _, p := range d.params {
		parts = append(parts, paramHelp(p))
	}
	return strings.Join(parts, " ")
}

// paramHelp renders one URI option as a sentence.
func paramHelp(p query.URIParam) string {
	opt := "?" + p.Name + "="
	if p.Keyspace {
		opt += " (keyspace)"
	}
	opt += ": " + p.Desc + "."
	if len(p.Values) > 0 {
		opt += " Values: " + strings.Join(p.Values, ", ") + "."
	}
	return opt
}

// wrapText breaks text into lines of at most 78 columns. A single word longer
// than a line stays whole.
func wrapText(text string) string {
	const limit = 78
	var lines []string
	line := ""
	for w := range strings.FieldsSeq(text) {
		if line != "" && len(line)+1+len(w) > limit {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	return strings.Join(append(lines, line), "\n")
}

// urlQuery parses the query string of a source URL. An unparseable URL yields
// false and not an error. Every caller treats it as "names nothing", which
// leaves the driver to report the real parse failure at open time.
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

// expectedSchemes renders the primary schemes of the registry as an error
// fragment ("expected mongodb:// or redis://"). So the message that an
// unsupported URL yields stays in sync with the drivers that exist.
func expectedSchemes() string {
	// Only connectable backends are suggested for a bad connection URL. A local
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

// driverFormatRow is the JSON shape of one dump format under `iq driver ls -v
// --json`. Auto reports whether content detection recognizes it. When false, the
// format must be forced with ?format=/--from-format.
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
// with its description, the URI schemes that select it, and its upstream docs. The
// global -v/--verbose appends the read-only file driver's dump-format catalogue.
func newDriverLsCmd(cfg *config) *cobra.Command {
	var jsonOut, yamlOut bool
	c := &cobra.Command{
		Use:   "ls",
		Short: "List the backend drivers iq can dispatch to",
		Long: "List the backend drivers iq can dispatch to. Each row shows the driver's stable\n" +
			"name (as `iq ls` reports it), a description, the URI schemes that select it, the\n" +
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

// listDrivers renders the driver registry as an aligned table (with a header row).
// With jsonOut/yamlOut set, it renders machine-readable output instead. verbose
// appends the dump-format catalogue of each format-bearing driver (the readers of the
// file driver).
func listDrivers(out io.Writer, verbose, jsonOut, yamlOut bool) error {
	if jsonOut || yamlOut {
		return writeStructured(out, driverRows(verbose), yamlOut)
	}
	if err := renderTable(out, driverTable()); err != nil {
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

// driverRows returns the machine-readable rows of the registry. With verbose, a
// driver that reads dump files lists its formats.
func driverRows(verbose bool) []driverRow {
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
			row.Formats = driverFormatRows(d.formats)
		}
		rows = append(rows, row)
	}
	return rows
}

// driverFormatRows returns the machine-readable rows of a driver's dump formats.
// It returns nil when the driver has none.
func driverFormatRows(formats []iqfile.FormatInfo) []driverFormatRow {
	var rows []driverFormatRow
	for _, fi := range formats {
		rows = append(rows, driverFormatRow{Name: fi.Name(), Auto: fi.Auto, Source: fi.Source})
	}
	return rows
}

// driverTable returns the table of the registry: a header row and one row for
// each driver.
func driverTable() [][]tableCell {
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
	return rows
}

// writeDriverFormats appends the dump-format catalogue of a driver under `iq driver ls
// -v`. First comes a caption that teaches the URL shape. Then comes an aligned
// name/source block, rendered as its own table so the column widths of the main grid
// are untouched. A content-detected format shows a bare name (usable as
// file:///<path>). A format that must be forced shows the full ?format=<name> to paste
// onto file:///<path>?format=<name>. A driver without formats gets nothing.
func writeDriverFormats(out io.Writer, d driver) error {
	if len(d.formats) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(out, "\n%s dump formats, a bare name auto-detects "+
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

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zsltg/iq/internal/query"
)

// mongoInspectCmds is the supported set of MongoDB diagnostic commands `inspect`
// runs. With no --only, it runs them all; --only narrows to the named ones.
var mongoInspectCmds = []string{"dbStats", "serverStatus", "listCollections", "collStats", "buildInfo", "hostInfo"}

// redisInfoCommonSections is the set of INFO sections a Redis server commonly
// reports. Unlike the other backends' lists it is advisory, not exhaustive — the
// authoritative set is whatever the live reply carries (redisInfoSections) — so
// it backs only the long help and --only completion, never validation.
var redisInfoCommonSections = []string{"server", "clients", "memory", "persistence", "stats", "replication", "cpu", "keyspace"}

// inspectSubcommands returns the --only candidates for a driver, mirroring the
// dispatch in newInspectCmd's RunE. It takes the canonical driver name so the
// caller can resolve it offline from a source URL, and returns nil for a driver
// with no introspection (file) or an unknown one.
func inspectSubcommands(driver string) []string {
	switch driver {
	case "mongo":
		return mongoInspectCmds
	case "redis":
		return redisInfoCommonSections
	case "cassandra":
		return cassandraInspectCmds
	case "dynamodb":
		return dynamoInspectCmds
	case "hbase":
		return hbaseInspectCmds
	case "couchdb":
		return couchInspectCmds
	case "couchbase":
		return couchbaseInspectCmds
	case "neo4j":
		return neo4jInspectCmds
	case "elasticsearch", "opensearch":
		return elasticInspectCmds
	default:
		return nil
	}
}

// newInspectCmd builds `iq inspect [source]`: show a source's native
// server/database introspection. The positional names the source (sq-style
// `<source>.<collection>` addressing); with none it uses --src or the active
// source. --only narrows the output. Bounded by --timeout.
func newInspectCmd(cfg *config) *cobra.Command {
	var (
		jsonOut bool
		yamlOut bool
		list    bool
		only    []string
	)
	long := "Show a source's native server/database introspection.\n\n" +
		"The positional argument names the source, like `iq inspect prod`; with none it\n" +
		"uses --src or the active source. MongoDB, Cassandra, DynamoDB, HBase, CouchDB,\n" +
		"Couchbase, and Neo4j sources accept sq-style `<source>.<collection>` / `<source>.<table>` /\n" +
		"`<source>.<database>` / `<source>.<label>` addressing (`iq inspect prod.books`) to\n" +
		"pick the collection/table/database/label, overriding the source URI's\n" +
		"?collection=/?table=/?database=/?label= default; Redis sources take no collection.\n\n" +
		"MongoDB — runs diagnostic database commands; no --only runs them all,\n" +
		"--only narrows to the named ones:\n" +
		"  " + strings.Join(mongoInspectCmds, "  ") + "\n" +
		"  (collStats needs a collection: address it as source.collection or set\n" +
		"  ?collection= on the source URI)\n\n" +
		"Cassandra — runs system-table reads; no --only runs them all, --only narrows:\n" +
		"  " + strings.Join(cassandraInspectCmds, "  ") + "\n" +
		"  (columns needs a table: address it as source.table or set ?table= on the\n" +
		"  source URI)\n\n" +
		"DynamoDB — runs introspection reads; no --only runs them all, --only narrows:\n" +
		"  " + strings.Join(dynamoInspectCmds, "  ") + "\n" +
		"  (table needs a table: address it as source.table or set ?table= on the\n" +
		"  source URI)\n\n" +
		"HBase — runs introspection reads; no --only runs them all, --only narrows:\n" +
		"  " + strings.Join(hbaseInspectCmds, "  ") + "\n" +
		"  (tables lists the source namespace's tables)\n\n" +
		"CouchDB — runs introspection reads; no --only runs them all, --only narrows:\n" +
		"  " + strings.Join(couchInspectCmds, "  ") + "\n" +
		"  (dbinfo and indexes need a database: address it as source.database or set\n" +
		"  ?database= on the source URI)\n\n" +
		"Couchbase — runs introspection reads; no --only runs them all, --only narrows:\n" +
		"  " + strings.Join(couchbaseInspectCmds, "  ") + "\n" +
		"  (collections needs a bucket: address it as source.collection or set ?bucket=\n" +
		"  on the source URI)\n\n" +
		"Neo4j — runs metadata procedures; no --only runs them all, --only narrows:\n" +
		"  " + strings.Join(neo4jInspectCmds, "  ") + "\n" +
		"  (all are database-level; labels lists the addressable collections)\n\n" +
		"Elasticsearch / OpenSearch — runs metadata reads; no --only runs them all, --only narrows:\n" +
		"  " + strings.Join(elasticInspectCmds, "  ") + "\n" +
		"  (mapping needs an index: address it as source.index or set ?index= on the\n" +
		"  source URI)\n\n" +
		"Redis — runs INFO; --only narrows it to those sections\n" +
		"(`iq inspect prod --only memory,server`), and none runs the full INFO. Common sections:\n" +
		"  " + strings.Join(redisInfoCommonSections, "  ") + "\n\n" +
		"Use -j/--json or -y/--yaml for machine-readable output, or --list to print the\n" +
		"subcommands/sections available for the source. The location header is redacted by\n" +
		"default: --reveal prints an inline password verbatim, --expand resolves a keyring-backed one."
	c := &cobra.Command{
		Use:               "inspect [source]",
		ValidArgsFunction: completeSourceHandles,
		Short:             "Show a source's native server/database introspection",
		Long:              long,
		Example: "  $ iq inspect                     # active source, database-level\n" +
			"  $ iq inspect shop                # a named source, database-level\n" +
			"  $ iq inspect shop.orders -j      # one collection (sq-style handle.collection)\n" +
			"  $ iq inspect shop --only dbStats,serverStatus # narrow db-level sections",
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
				return err
			}
			defer func() { _ = st.Close() }()

			out := cmd.OutOrStdout()
			switch driverName(cfg.url) {
			case "redis":
				return inspectRedis(ctx, out, st, cfg, only, jsonOut, yamlOut, list)
			case "cassandra":
				return inspectCassandra(ctx, out, st, cfg, only, jsonOut, yamlOut, list)
			case "dynamodb":
				return inspectDynamo(ctx, out, st, cfg, only, jsonOut, yamlOut, list)
			case "hbase":
				return inspectHBase(ctx, out, st, cfg, only, jsonOut, yamlOut, list)
			case "couchdb":
				return inspectCouch(ctx, out, st, cfg, only, jsonOut, yamlOut, list)
			case "couchbase":
				return inspectCouchbase(ctx, out, st, cfg, only, jsonOut, yamlOut, list)
			case "neo4j":
				return inspectNeo4j(ctx, out, st, cfg, only, jsonOut, yamlOut, list)
			case "elasticsearch", "opensearch":
				return inspectElastic(ctx, out, st, cfg, only, jsonOut, yamlOut, list)
			case "file":
				// inspect reports live server metadata; a dump file has none. Point
				// the user at the operations that do work on a file source.
				return errors.New("inspect reports live server metadata, and a file source has none; " +
					"query it with a jq filter (`iq '.[]' --src <name>`) or compare it with `iq diff`")
			default:
				return inspectMongo(ctx, out, st, cfg, only, jsonOut, yamlOut, list)
			}
		},
	}
	c.Flags().BoolVarP(&jsonOut, "json", "j", false, "emit machine-readable JSON")
	c.Flags().BoolVarP(&yamlOut, "yaml", "y", false, "emit machine-readable YAML")
	c.MarkFlagsMutuallyExclusive("json", "yaml")
	c.Flags().BoolVar(&list, "list", false, "list the subcommands/sections available for the source")
	c.Flags().StringSliceVar(&only, "only", nil, "narrow to these sections or subcommands (--list names the source's set)")
	// --only's candidates depend on the source's backend, which the completion
	// derives offline from the stored URL scheme; the error can only fire for an
	// unknown flag name, so swallowing it keeps setup panic-free (as at root).
	_ = c.RegisterFlagCompletionFunc("only", completeInspectOnly)
	c.Flags().BoolVar(&cfg.reveal, "reveal", false, "print an inline-stored password verbatim in the location header instead of redacting it")
	c.Flags().BoolVar(&cfg.expand, "expand", false, "resolve a keyring-backed password and inline it in the location header")
	return c
}

// inspectResult pairs a subcommand/section name with its rendered reply, the
// unit every driver's inspector collects before rendering.
type inspectResult struct {
	sub   string
	value any
}

// renderInspectResults emits collected inspect results: a name→value map under
// -j/-y, else the redacted location header followed by each reply in the store's
// native form. It is the shared tail of every non-Redis inspector, so a new
// backend's inspector only assembles its results and calls this.
func renderInspectResults(out io.Writer, st store, cfg *config, results []inspectResult, jsonOut, yamlOut bool) error {
	if jsonOut || yamlOut {
		byName := make(map[string]any, len(results))
		for _, r := range results {
			byName[r.sub] = r.value
		}
		return writeStructured(out, byName, yamlOut)
	}
	if err := inspectHeader(out, cfg); err != nil {
		return err
	}
	for _, r := range results {
		if _, err := fmt.Fprintf(out, "%s\n%s\n\n", pal.header.Sprint("# "+r.sub), st.FormatRaw(r.value, colorOn())); err != nil {
			return err
		}
	}
	return nil
}

// inspectRedis runs INFO (narrowed to the given sections) and renders it. With
// list, it prints the section names the reply exposes instead of the reply.
func inspectRedis(ctx context.Context, out io.Writer, st store, cfg *config, sections []string, jsonOut, yamlOut, list bool) error {
	res, err := query.NewRunner(st).Run(ctx, append([]string{"INFO"}, sections...))
	if err != nil {
		return redactErr(err, cfg.url)
	}
	info, _ := res.(string)
	if list {
		return writeInspectList(out, redisInfoSections(info), jsonOut, yamlOut)
	}
	if jsonOut || yamlOut {
		return writeStructured(out, parseRedisInfo(info), yamlOut)
	}
	if err := inspectHeader(out, cfg); err != nil {
		return err
	}
	_, err = io.WriteString(out, info)
	return err
}

// redisInfoSections returns the section names present in an INFO reply, sorted.
func redisInfoSections(info string) []string {
	parsed := parseRedisInfo(info)
	names := make([]string, 0, len(parsed))
	for name := range parsed {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// parseRedisInfo turns an INFO reply into section → key → value. Lines like
// "# Server" open a section; "key:value" lines populate it.
func parseRedisInfo(info string) map[string]map[string]string {
	sections := make(map[string]map[string]string)
	section := "default"
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			section = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if sections[section] == nil {
			sections[section] = make(map[string]string)
		}
		sections[section][k] = v
	}
	return sections
}

// inspectMongo runs the requested MongoDB diagnostic commands (all supported when
// none are named) and renders each reply keyed by subcommand. With list, it prints
// the supported subcommand names instead, without touching the store.
func inspectMongo(ctx context.Context, out io.Writer, st store, cfg *config, subs []string, jsonOut, yamlOut, list bool) error {
	if list {
		return writeInspectList(out, mongoInspectCmds, jsonOut, yamlOut)
	}
	explicit := len(subs) > 0
	which := subs
	if !explicit {
		which = mongoInspectCmds
	}
	for _, sub := range which {
		if !isMongoInspectCmd(sub) {
			return fmt.Errorf("unknown inspect subcommand %q; want one of %s", sub, strings.Join(mongoInspectCmds, ", "))
		}
	}

	coll := mongoCollection(cfg)
	results := make([]inspectResult, 0, len(which))
	for _, sub := range which {
		if sub == "collStats" && coll == "" {
			if explicit {
				return fmt.Errorf("collStats needs a collection; address it as handle.collection or set ?collection= on the source URI")
			}
			continue // skip in the run-all case
		}
		res, err := query.NewRunner(st).Run(ctx, []string{mongoInspectDoc(sub, coll)})
		if err != nil {
			res = map[string]any{"error": redactErr(err, cfg.url).Error()}
		}
		results = append(results, inspectResult{sub: sub, value: res})
	}

	return renderInspectResults(out, st, cfg, results, jsonOut, yamlOut)
}

// mongoInspectDoc builds the JSON command document for a MongoDB diagnostic
// subcommand. collStats takes the collection name; the rest take 1.
func mongoInspectDoc(sub, collection string) string {
	if sub == "collStats" {
		return fmt.Sprintf(`{%q:%q}`, sub, collection)
	}
	return fmt.Sprintf(`{%q:1}`, sub)
}

// isMongoInspectCmd reports whether sub is a supported diagnostic command.
func isMongoInspectCmd(sub string) bool {
	for _, c := range mongoInspectCmds {
		if c == sub {
			return true
		}
	}
	return false
}

// cassandraInspectCmds is the supported set of Cassandra system-table reads inspect
// runs. With no --only it runs them all; --only narrows to the named ones.
var cassandraInspectCmds = []string{"local", "tables", "columns"}

// inspectCassandra runs the requested Cassandra system-table reads (all supported
// when none are named) and renders each reply keyed by subcommand. "local" shows the
// coordinator's cluster and version row; "tables" lists the keyspace's tables;
// "columns" describes the selected table's columns. With list, it prints the
// supported names without touching the store.
func inspectCassandra(ctx context.Context, out io.Writer, st store, cfg *config, subs []string, jsonOut, yamlOut, list bool) error {
	if list {
		return writeInspectList(out, cassandraInspectCmds, jsonOut, yamlOut)
	}
	explicit := len(subs) > 0
	which := subs
	if !explicit {
		which = cassandraInspectCmds
	}
	for _, sub := range which {
		if !isCassandraInspectCmd(sub) {
			return fmt.Errorf("unknown inspect subcommand %q; want one of %s", sub, strings.Join(cassandraInspectCmds, ", "))
		}
	}

	keyspace, table := cassandraTarget(cfg)
	results := make([]inspectResult, 0, len(which))
	for _, sub := range which {
		stmt, ok := cassandraInspectStmt(sub, keyspace, table)
		if !ok {
			if explicit {
				return fmt.Errorf("columns needs a table; address it as handle.table or set ?table= on the source URI")
			}
			continue // skip in the run-all case
		}
		res, err := query.NewRunner(st).Run(ctx, []string{stmt})
		if err != nil {
			res = map[string]any{"error": redactErr(err, cfg.url).Error()}
		}
		results = append(results, inspectResult{sub: sub, value: res})
	}

	return renderInspectResults(out, st, cfg, results, jsonOut, yamlOut)
}

// cassandraInspectStmt builds the CQL for a Cassandra diagnostic subcommand, keyed
// off the system schema. columns needs a table and reports false when none is
// selected. The keyspace and table are interpolated as quoted string literals, so
// the statement is injection-safe.
func cassandraInspectStmt(sub, keyspace, table string) (string, bool) {
	switch sub {
	case "local":
		return "SELECT cluster_name, release_version, cql_version FROM system.local", true
	case "tables":
		return fmt.Sprintf("SELECT table_name FROM system_schema.tables WHERE keyspace_name = %s", cqlString(keyspace)), true
	case "columns":
		if table == "" {
			return "", false
		}
		return fmt.Sprintf("SELECT column_name, kind, type FROM system_schema.columns WHERE keyspace_name = %s AND table_name = %s",
			cqlString(keyspace), cqlString(table)), true
	default:
		return "", false
	}
}

// isCassandraInspectCmd reports whether sub is a supported diagnostic read.
func isCassandraInspectCmd(sub string) bool {
	for _, c := range cassandraInspectCmds {
		if c == sub {
			return true
		}
	}
	return false
}

// cqlString renders s as a single-quoted CQL string literal, doubling an embedded
// quote so an interpolated keyspace or table name cannot break out of the literal.
func cqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// dynamoInspectCmds is the supported set of DynamoDB introspection reads inspect runs.
// With no --only it runs them all; --only narrows to the named ones.
var dynamoInspectCmds = []string{"tables", "table"}

// dynamoInspector is the introspection capability inspectDynamo needs from the store.
// DynamoDB's introspection is API calls (ListTables, DescribeTable), not a PartiQL
// statement, so unlike Cassandra it is a driver method the CLI calls directly rather
// than a statement routed through the raw Query path.
type dynamoInspector interface {
	InspectTables(ctx context.Context) (any, error)
	InspectTable(ctx context.Context) (any, error)
}

// inspectDynamo runs the requested DynamoDB introspection reads (all supported when
// none are named) and renders each reply keyed by subcommand. "tables" lists the
// region's tables; "table" describes the selected table's schema and size. With list,
// it prints the supported names without touching the store.
func inspectDynamo(ctx context.Context, out io.Writer, st store, cfg *config, subs []string, jsonOut, yamlOut, list bool) error {
	if list {
		return writeInspectList(out, dynamoInspectCmds, jsonOut, yamlOut)
	}
	di, ok := st.(dynamoInspector)
	if !ok {
		return errors.New("inspect is not supported for this source")
	}
	explicit := len(subs) > 0
	which := subs
	if !explicit {
		which = dynamoInspectCmds
	}
	for _, sub := range which {
		if sub != "tables" && sub != "table" {
			return fmt.Errorf("unknown inspect subcommand %q; want one of %s", sub, strings.Join(dynamoInspectCmds, ", "))
		}
	}

	results := make([]inspectResult, 0, len(which))
	for _, sub := range which {
		var (
			res any
			err error
		)
		switch sub {
		case "tables":
			res, err = di.InspectTables(ctx)
		case "table":
			res, err = di.InspectTable(ctx)
			if err != nil && !explicit {
				continue // a source with no table selected skips "table" in the run-all case
			}
		}
		if err != nil {
			res = map[string]any{"error": redactErr(err, cfg.url).Error()}
		}
		results = append(results, inspectResult{sub: sub, value: res})
	}

	return renderInspectResults(out, st, cfg, results, jsonOut, yamlOut)
}

// hbaseInspectCmds is the supported set of HBase introspection reads inspect runs.
// HBase exposes no per-table column-family descriptor through the client RPC, so the
// set is table listing only.
var hbaseInspectCmds = []string{"tables"}

// hbaseInspector is the introspection capability inspectHBase needs from the store.
// HBase introspection is an admin RPC (ListTableNames), not a query-language
// statement, so like DynamoDB it is a driver method the CLI calls directly rather than
// a statement routed through Query.
type hbaseInspector interface {
	InspectTables(ctx context.Context) (any, error)
}

// inspectHBase runs the HBase introspection reads (currently just "tables", which
// lists the source namespace's tables) and renders each reply keyed by subcommand.
// With list, it prints the supported names without touching the store.
func inspectHBase(ctx context.Context, out io.Writer, st store, cfg *config, subs []string, jsonOut, yamlOut, list bool) error {
	if list {
		return writeInspectList(out, hbaseInspectCmds, jsonOut, yamlOut)
	}
	hi, ok := st.(hbaseInspector)
	if !ok {
		return errors.New("inspect is not supported for this source")
	}
	which := subs
	if len(which) == 0 {
		which = hbaseInspectCmds
	}
	for _, sub := range which {
		if sub != "tables" {
			return fmt.Errorf("unknown inspect subcommand %q; want one of %s", sub, strings.Join(hbaseInspectCmds, ", "))
		}
	}

	results := make([]inspectResult, 0, len(which))
	for _, sub := range which {
		res, err := hi.InspectTables(ctx)
		if err != nil {
			res = map[string]any{"error": redactErr(err, cfg.url).Error()}
		}
		results = append(results, inspectResult{sub: sub, value: res})
	}

	return renderInspectResults(out, st, cfg, results, jsonOut, yamlOut)
}

// couchInspectCmds is the supported set of CouchDB introspection reads `inspect`
// runs. "server" and "databases" are server-level; "dbinfo" and "indexes" need a
// database selected. With no --only it runs them all; --only narrows.
var couchInspectCmds = []string{"server", "databases", "dbinfo", "indexes"}

// couchInspector is the introspection capability inspectCouch needs from the store.
// CouchDB's introspection is HTTP API reads (GET /, GET /_all_dbs, GET /{db}, GET
// /{db}/_index), not a Mango query, so it is a driver method the CLI calls directly
// rather than a statement routed through the raw Query path.
type couchInspector interface {
	InspectServer(ctx context.Context) (any, error)
	InspectDatabases(ctx context.Context) (any, error)
	InspectDBInfo(ctx context.Context) (any, error)
	InspectIndexes(ctx context.Context) (any, error)
}

// inspectCouch runs the requested CouchDB introspection reads (all supported when
// none are named) and renders each reply keyed by subcommand. "dbinfo" and "indexes"
// need a database selected and are skipped in the run-all case when none is. With
// list, it prints the supported names without touching the store.
func inspectCouch(ctx context.Context, out io.Writer, st store, cfg *config, subs []string, jsonOut, yamlOut, list bool) error {
	if list {
		return writeInspectList(out, couchInspectCmds, jsonOut, yamlOut)
	}
	ci, ok := st.(couchInspector)
	if !ok {
		return errors.New("inspect is not supported for this source")
	}
	explicit := len(subs) > 0
	which := subs
	if !explicit {
		which = couchInspectCmds
	}
	for _, sub := range which {
		if !isCouchInspectCmd(sub) {
			return fmt.Errorf("unknown inspect subcommand %q; want one of %s", sub, strings.Join(couchInspectCmds, ", "))
		}
	}

	results := make([]inspectResult, 0, len(which))
	for _, sub := range which {
		var (
			res any
			err error
		)
		switch sub {
		case "server":
			res, err = ci.InspectServer(ctx)
		case "databases":
			res, err = ci.InspectDatabases(ctx)
		case "dbinfo":
			res, err = ci.InspectDBInfo(ctx)
			if err != nil && !explicit {
				continue // a source with no database selected skips "dbinfo" in run-all
			}
		case "indexes":
			res, err = ci.InspectIndexes(ctx)
			if err != nil && !explicit {
				continue // likewise "indexes"
			}
		}
		if err != nil {
			res = map[string]any{"error": redactErr(err, cfg.url).Error()}
		}
		results = append(results, inspectResult{sub: sub, value: res})
	}

	return renderInspectResults(out, st, cfg, results, jsonOut, yamlOut)
}

// isCouchInspectCmd reports whether sub is a supported CouchDB inspect subcommand.
func isCouchInspectCmd(sub string) bool {
	for _, c := range couchInspectCmds {
		if c == sub {
			return true
		}
	}
	return false
}

// couchbaseInspectCmds is the supported set of Couchbase introspection reads `inspect`
// runs. "cluster", "buckets", and "indexes" are cluster-level; "collections" needs a
// bucket selected. With no --only it runs them all; --only narrows.
var couchbaseInspectCmds = []string{"cluster", "buckets", "collections", "indexes"}

// couchbaseInspector is the introspection capability inspectCouchbase needs from the
// store: SDK manager reads (system:nodes, GetAllBuckets, GetAllScopes) and a
// system:indexes query, called directly rather than routed through the raw Query path.
type couchbaseInspector interface {
	InspectCluster(ctx context.Context) (any, error)
	InspectBuckets(ctx context.Context) (any, error)
	InspectCollections(ctx context.Context) (any, error)
	InspectIndexes(ctx context.Context) (any, error)
}

// inspectCouchbase runs the requested Couchbase introspection reads (all supported when
// none are named) and renders each reply keyed by subcommand. "collections" needs a
// bucket selected and is skipped in the run-all case when none is. With list, it prints
// the supported names without touching the store.
func inspectCouchbase(ctx context.Context, out io.Writer, st store, cfg *config, subs []string, jsonOut, yamlOut, list bool) error {
	if list {
		return writeInspectList(out, couchbaseInspectCmds, jsonOut, yamlOut)
	}
	ci, ok := st.(couchbaseInspector)
	if !ok {
		return errors.New("inspect is not supported for this source")
	}
	explicit := len(subs) > 0
	which := subs
	if !explicit {
		which = couchbaseInspectCmds
	}
	for _, sub := range which {
		if !isCouchbaseInspectCmd(sub) {
			return fmt.Errorf("unknown inspect subcommand %q; want one of %s", sub, strings.Join(couchbaseInspectCmds, ", "))
		}
	}

	results := make([]inspectResult, 0, len(which))
	for _, sub := range which {
		var (
			res any
			err error
		)
		switch sub {
		case "cluster":
			res, err = ci.InspectCluster(ctx)
		case "buckets":
			res, err = ci.InspectBuckets(ctx)
		case "collections":
			res, err = ci.InspectCollections(ctx)
			if err != nil && !explicit {
				continue // a source with no bucket selected skips "collections" in run-all
			}
		case "indexes":
			res, err = ci.InspectIndexes(ctx)
		}
		if err != nil {
			res = map[string]any{"error": redactErr(err, cfg.url).Error()}
		}
		results = append(results, inspectResult{sub: sub, value: res})
	}

	return renderInspectResults(out, st, cfg, results, jsonOut, yamlOut)
}

// isCouchbaseInspectCmd reports whether sub is a supported Couchbase inspect subcommand.
func isCouchbaseInspectCmd(sub string) bool {
	for _, c := range couchbaseInspectCmds {
		if c == sub {
			return true
		}
	}
	return false
}

// elasticInspectCmds is the supported set of Elasticsearch introspection reads
// `inspect` runs. "server", "indices", and "aliases" are server-level; "mapping"
// needs an index selected. With no --only it runs them all; --only narrows.
var elasticInspectCmds = []string{"server", "indices", "mapping", "aliases"}

// elasticInspector is the introspection capability inspectElastic needs from the
// store. Elasticsearch's introspection is HTTP API reads (GET /, GET /_cat/indices,
// GET /{index}/_mapping, GET /_cat/aliases), not a search, so it is a driver method
// the CLI calls directly rather than a query routed through the raw Query path.
type elasticInspector interface {
	InspectServer(ctx context.Context) (any, error)
	InspectIndices(ctx context.Context) (any, error)
	InspectMapping(ctx context.Context) (any, error)
	InspectAliases(ctx context.Context) (any, error)
}

// inspectElastic runs the requested Elasticsearch introspection reads (all supported
// when none are named) and renders each reply keyed by subcommand. "mapping" needs an
// index selected and is skipped in the run-all case when none is. With list, it
// prints the supported names without touching the store.
func inspectElastic(ctx context.Context, out io.Writer, st store, cfg *config, subs []string, jsonOut, yamlOut, list bool) error {
	if list {
		return writeInspectList(out, elasticInspectCmds, jsonOut, yamlOut)
	}
	ei, ok := st.(elasticInspector)
	if !ok {
		return errors.New("inspect is not supported for this source")
	}
	explicit := len(subs) > 0
	which := subs
	if !explicit {
		which = elasticInspectCmds
	}
	for _, sub := range which {
		if !isElasticInspectCmd(sub) {
			return fmt.Errorf("unknown inspect subcommand %q; want one of %s", sub, strings.Join(elasticInspectCmds, ", "))
		}
	}

	results := make([]inspectResult, 0, len(which))
	for _, sub := range which {
		var (
			res any
			err error
		)
		switch sub {
		case "server":
			res, err = ei.InspectServer(ctx)
		case "indices":
			res, err = ei.InspectIndices(ctx)
		case "mapping":
			res, err = ei.InspectMapping(ctx)
			if err != nil && !explicit {
				continue // a source with no index selected skips "mapping" in run-all
			}
		case "aliases":
			res, err = ei.InspectAliases(ctx)
		}
		if err != nil {
			res = map[string]any{"error": redactErr(err, cfg.url).Error()}
		}
		results = append(results, inspectResult{sub: sub, value: res})
	}

	return renderInspectResults(out, st, cfg, results, jsonOut, yamlOut)
}

// isElasticInspectCmd reports whether sub is a supported Elasticsearch inspect subcommand.
func isElasticInspectCmd(sub string) bool {
	for _, c := range elasticInspectCmds {
		if c == sub {
			return true
		}
	}
	return false
}

// neo4jInspectCmds is the supported set of Neo4j introspection reads `inspect` runs.
// All are database-level metadata procedures, so none needs a label selected. With
// no --only it runs them all; --only narrows.
var neo4jInspectCmds = []string{"server", "databases", "labels", "reltypes", "constraints"}

// neo4jInspector is the introspection capability inspectNeo4j needs from the store.
// Neo4j's introspection is metadata procedures (dbms.components, db.labels,
// db.relationshipTypes, SHOW DATABASES, SHOW CONSTRAINTS), which the driver runs
// itself rather than routing an arbitrary statement through the raw Query path.
type neo4jInspector interface {
	InspectServer(ctx context.Context) (any, error)
	InspectDatabases(ctx context.Context) (any, error)
	InspectLabels(ctx context.Context) (any, error)
	InspectRelationshipTypes(ctx context.Context) (any, error)
	InspectConstraints(ctx context.Context) (any, error)
}

// inspectNeo4j runs the requested Neo4j introspection reads (all supported when none
// are named) and renders each reply keyed by subcommand. Every read is database-level,
// so none is skipped for a missing label. With list, it prints the supported names
// without touching the store.
func inspectNeo4j(ctx context.Context, out io.Writer, st store, cfg *config, subs []string, jsonOut, yamlOut, list bool) error {
	if list {
		return writeInspectList(out, neo4jInspectCmds, jsonOut, yamlOut)
	}
	ni, ok := st.(neo4jInspector)
	if !ok {
		return errors.New("inspect is not supported for this source")
	}
	which := subs
	if len(which) == 0 {
		which = neo4jInspectCmds
	}
	for _, sub := range which {
		if !isNeo4jInspectCmd(sub) {
			return fmt.Errorf("unknown inspect subcommand %q; want one of %s", sub, strings.Join(neo4jInspectCmds, ", "))
		}
	}

	results := make([]inspectResult, 0, len(which))
	for _, sub := range which {
		var (
			res any
			err error
		)
		switch sub {
		case "server":
			res, err = ni.InspectServer(ctx)
		case "databases":
			res, err = ni.InspectDatabases(ctx)
		case "labels":
			res, err = ni.InspectLabels(ctx)
		case "reltypes":
			res, err = ni.InspectRelationshipTypes(ctx)
		case "constraints":
			res, err = ni.InspectConstraints(ctx)
		}
		if err != nil {
			res = map[string]any{"error": redactErr(err, cfg.url).Error()}
		}
		results = append(results, inspectResult{sub: sub, value: res})
	}

	return renderInspectResults(out, st, cfg, results, jsonOut, yamlOut)
}

// isNeo4jInspectCmd reports whether sub is a supported Neo4j inspect subcommand.
func isNeo4jInspectCmd(sub string) bool {
	for _, c := range neo4jInspectCmds {
		if c == sub {
			return true
		}
	}
	return false
}

// writeInspectList renders the names --list emits: a JSON or YAML array with
// jsonOut/yamlOut, else one name per line.
func writeInspectList(out io.Writer, names []string, jsonOut, yamlOut bool) error {
	if jsonOut || yamlOut {
		return writeStructured(out, names, yamlOut)
	}
	for _, name := range names {
		if _, err := fmt.Fprintln(out, name); err != nil {
			return err
		}
	}
	return nil
}

// inspectHeader writes a one-line source header: driver and location. The
// location is redacted unless --reveal (inline password) or --expand (keyring
// password) un-redacts it, rendered from the stored source like `iq ls`.
func inspectHeader(out io.Writer, cfg *config) error {
	loc := displayLocation(cfg.source, cfg.handle, cfg.reveal, cfg.expand)
	header := fmt.Sprintf("%s  %s", driverName(cfg.source.URL), loc)
	_, err := fmt.Fprintf(out, "%s\n\n", pal.header.Sprint(header))
	return err
}

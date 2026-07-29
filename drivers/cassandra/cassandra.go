// Package cassandra adapts an Apache Cassandra table to the query ports. A table
// is modelled as Map[key, row]: a row's full primary key (partition-key columns
// followed by clustering columns), rendered to a string, is the key; the row, each
// column normalized to a JSON-ready value, is the value. The jq filter runs
// client-side over rows fetched by primary key or streamed from the table, so the
// semantics match every other backend behind the KV port.
//
// A single-column primary key renders as its bare canonical string (like Mongo's
// _id); a composite primary key renders as a JSON array of the key columns in
// schema order. Reversing a key back to typed bind values needs the table schema,
// which is read once at connect time.
package cassandra

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/render"
)

// scanBatch is the default page size for ScanBatches: how many rows to accumulate
// before handing a page to the caller, bounding streaming memory. It is also the
// fetch size handed to the driver's server-side paging.
const scanBatch = 100

// defaultPort is the CQL native transport port a host without an explicit port
// falls back to.
const defaultPort = "9042"

// errNoTable is returned when a table-scoped operation runs without a table
// selected. It is a sentinel so the CLI can surface a clear hint.
var errNoTable = errors.New("cassandra: no table selected; address it as handle.table or set ?table= in the source url")

// Store adapts one Cassandra keyspace to the query ports. The jq and write paths
// are scoped to a single table (the keyspace of the KV model); the raw path runs
// arbitrary CQL and needs no table.
type Store struct {
	session  *gocql.Session
	keyspace string
	table    string
	meta     *gocql.TableMetadata
	pageSize int
	decimal  numfmt.DecimalMode
}

// connConfig is the parsed form of a cassandra:// source URL.
type connConfig struct {
	hosts       []string
	keyspace    string
	table       string
	username    string
	password    string
	consistency gocql.Consistency
}

// Open connects to the Cassandra cluster named by a cassandra:// URL and verifies
// the connection by establishing a session (which fails fast on a bad URL or an
// unreachable cluster). The keyspace is taken from the URL path; the table (the jq
// keyspace, may be empty for raw-only use) is the address override when non-empty,
// else the URL's ?table= default. When a table is selected its schema is read once
// so keys can be encoded and reversed. When trace is non-nil, each executed CQL
// statement is logged to it (the CLI's --verbose trace) with bind values redacted.
// dec chooses how decimal values are presented to the filter.
func Open(ctx context.Context, rawURL, address string, trace io.Writer, dec numfmt.DecimalMode) (*Store, error) {
	cc, err := parseURL(rawURL, address)
	if err != nil {
		return nil, err
	}
	cluster := gocql.NewCluster(cc.hosts...)
	cluster.Keyspace = cc.keyspace
	cluster.Consistency = cc.consistency
	cluster.Authenticator = authenticatorFor(cc)
	// Bound connection setup and per-query time by the caller's deadline, so a
	// stalled cluster never hangs the command (an infinite wait is forbidden).
	if dl, ok := ctx.Deadline(); ok {
		d := time.Until(dl)
		cluster.ConnectTimeout = d
		cluster.Timeout = d
	}
	if trace != nil {
		cluster.QueryObserver = newQueryObserver(trace)
	}
	session, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("connect cassandra: %w", err)
	}
	st := &Store{
		session:  session,
		keyspace: cc.keyspace,
		table:    cc.table,
		pageSize: scanBatch,
		decimal:  dec,
	}
	if cc.table != "" {
		meta, err := tableMeta(session, cc.keyspace, cc.table)
		if err != nil {
			session.Close()
			return nil, err
		}
		st.meta = meta
	}
	return st, nil
}

// Target returns the keyspace and table a cassandra:// source addresses: the
// address override wins over the URL's ?table= default. It lets the CLI show and
// reason about a source's keyspace and table without duplicating the URL parsing.
func Target(rawURL, address string) (keyspace, table string, err error) {
	cc, err := parseURL(rawURL, address)
	if err != nil {
		return "", "", err
	}
	return cc.keyspace, cc.table, nil
}

// authenticatorFor returns the SASL authenticator for a connection: a password
// authenticator when the URL carries a username, else nil (anonymous, gocql's
// default). It is split out so the credential-wiring decision is unit-testable
// without opening a session.
func authenticatorFor(cc connConfig) gocql.Authenticator {
	if cc.username == "" {
		return nil
	}
	return gocql.PasswordAuthenticator{Username: cc.username, Password: cc.password}
}

// parseURL parses a cassandra:// source URL into its cluster config. It is
// hand-rolled rather than net/url-based because a multi-host authority
// (cassandra://h1,h2/ks) is not a valid net/url host. The address override, when
// non-empty, wins over the URL's ?table= default. The keyspace is required, as both
// the jq and raw paths run against a specific keyspace.
func parseURL(rawURL, address string) (connConfig, error) {
	const scheme = "cassandra://"
	if !strings.HasPrefix(rawURL, scheme) {
		return connConfig{}, fmt.Errorf("cassandra url must start with %s", scheme)
	}
	rest := rawURL[len(scheme):]

	rest, rawQuery, _ := strings.Cut(rest, "?")
	authority, path, _ := strings.Cut(rest, "/")

	var username, password string
	if userinfo, hostpart, ok := strings.Cut(authority, "@"); ok {
		username, password, _ = strings.Cut(userinfo, ":")
		authority = hostpart
	}
	if authority == "" {
		return connConfig{}, fmt.Errorf("cassandra url must name at least one host, e.g. cassandra://host:9042/keyspace")
	}
	hosts := strings.Split(authority, ",")
	for i, h := range hosts {
		hosts[i] = withPort(h)
	}

	keyspace := strings.Trim(path, "/")
	if keyspace == "" || strings.Contains(keyspace, "/") {
		return connConfig{}, fmt.Errorf("cassandra url must name a keyspace, e.g. cassandra://host:9042/mykeyspace")
	}

	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return connConfig{}, fmt.Errorf("parse cassandra url query: %w", err)
	}
	table := address
	if table == "" {
		table = q.Get("table")
	}
	consistency, err := parseConsistency(q.Get("consistency"))
	if err != nil {
		return connConfig{}, err
	}
	return connConfig{
		hosts:       hosts,
		keyspace:    keyspace,
		table:       table,
		username:    username,
		password:    password,
		consistency: consistency,
	}, nil
}

// withPort appends the default CQL port to a bare host, leaving a host that already
// carries a port (or an IPv6 literal with one) untouched.
func withPort(host string) string {
	if host == "" {
		return host
	}
	// A bracketed IPv6 literal carries its port after the closing bracket.
	if strings.HasPrefix(host, "[") {
		if strings.Contains(host, "]:") {
			return host
		}
		return host + ":" + defaultPort
	}
	if strings.Contains(host, ":") {
		return host
	}
	return host + ":" + defaultPort
}

// parseConsistency resolves the optional ?consistency= URL parameter to a gocql
// consistency level, case-insensitively, defaulting to QUORUM when absent. An
// unknown level fails fast at connect rather than surprising a later query.
func parseConsistency(s string) (gocql.Consistency, error) {
	if s == "" {
		return gocql.Quorum, nil
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "any":
		return gocql.Any, nil
	case "one":
		return gocql.One, nil
	case "two":
		return gocql.Two, nil
	case "three":
		return gocql.Three, nil
	case "quorum":
		return gocql.Quorum, nil
	case "all":
		return gocql.All, nil
	case "localquorum", "local_quorum":
		return gocql.LocalQuorum, nil
	case "eachquorum", "each_quorum":
		return gocql.EachQuorum, nil
	case "localone", "local_one":
		return gocql.LocalOne, nil
	default:
		return gocql.Quorum, fmt.Errorf("cassandra: unknown consistency %q", s)
	}
}

// tableMeta reads a table's schema from the keyspace metadata, so keys can be
// encoded and reversed and writes can coerce values to column types. A keyspace or
// table that does not exist is an error the CLI surfaces cleanly.
func tableMeta(session *gocql.Session, keyspace, table string) (*gocql.TableMetadata, error) {
	ks, err := session.KeyspaceMetadata(keyspace)
	if err != nil {
		return nil, fmt.Errorf("cassandra keyspace metadata: %w", err)
	}
	meta, ok := ks.Tables[table]
	if !ok {
		return nil, fmt.Errorf("cassandra: table %q not found in keyspace %q", table, keyspace)
	}
	return meta, nil
}

// Get fetches the rows whose primary key matches one of keys and returns them keyed
// by the same string key. A key with no row is absent from the map. A single-column primary
// key uses one WHERE ... IN query; a composite key uses one point query per key
// (each bounded by ctx). Empty keys short-circuit with no round-trip.
func (s *Store) Get(ctx context.Context, keys []string) (map[string]any, error) {
	if s.meta == nil {
		return nil, errNoTable
	}
	out := make(map[string]any, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	pk := primaryKeyColumns(s.meta)
	var err error
	if len(pk) == 1 {
		err = s.getSingle(ctx, pk[0], keys, out)
	} else {
		err = s.getComposite(ctx, pk, keys, out)
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// getSingle fetches every requested key of a single-column-keyed table in one
// WHERE ... IN query, the efficient primary-key membership read.
func (s *Store) getSingle(ctx context.Context, col *gocql.ColumnMetadata, keys []string, out map[string]any) error {
	binds := make([]any, 0, len(keys))
	for _, k := range keys {
		vals, err := decodeKey(s.meta, k)
		if err != nil {
			return err
		}
		binds = append(binds, vals[0])
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(binds)), ",")
	cql := fmt.Sprintf("SELECT * FROM %s WHERE %s IN (%s)", s.tableRef(), quoteIdent(col.Name), placeholders)
	iter := s.session.Query(cql, binds...).IterContext(ctx)
	return s.drain(iter, out)
}

// getComposite fetches each requested composite key with its own point query, since
// a composite primary key cannot be expressed as a single IN membership test.
func (s *Store) getComposite(ctx context.Context, pk []*gocql.ColumnMetadata, keys []string, out map[string]any) error {
	conds := make([]string, len(pk))
	for i, c := range pk {
		conds[i] = quoteIdent(c.Name) + " = ?"
	}
	cql := fmt.Sprintf("SELECT * FROM %s WHERE %s", s.tableRef(), strings.Join(conds, " AND "))
	for _, k := range keys {
		binds, err := decodeKey(s.meta, k)
		if err != nil {
			return err
		}
		iter := s.session.Query(cql, binds...).IterContext(ctx)
		if err := s.drain(iter, out); err != nil {
			return err
		}
	}
	return nil
}

// drain reads every row an iterator yields into out, keyed and normalized, then
// closes the iterator. It is the shared consumer of the point-read queries.
func (s *Store) drain(iter *gocql.Iter, out map[string]any) error {
	row := map[string]any{}
	for iter.MapScan(row) {
		out[KeyOf(s.meta, row)] = normalizeRow(row, s.decimal)
		row = map[string]any{}
	}
	if err := iter.Close(); err != nil {
		return fmt.Errorf("cassandra select: %w", err)
	}
	return nil
}

// ScanBatches streams the whole table, handing the caller each page of {key: row}
// as the server-side cursor yields it, so a streaming caller keeps only one page in
// memory. Bounded by ctx; stops at the first error from fn or the driver.
func (s *Store) ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error {
	if s.meta == nil {
		return errNoTable
	}
	cql := fmt.Sprintf("SELECT * FROM %s", s.tableRef())
	return s.pageScan(ctx, cql, nil, fn)
}

// pageScan runs a paged SELECT and hands the caller a page of {key: row} every
// pageSize rows, the shared body of ScanBatches and ScanFiltered. The driver
// transparently fetches successive server pages as the iterator advances; the
// pageSize batching here is the streaming contract to fn.
func (s *Store) pageScan(ctx context.Context, cql string, binds []any, fn func(batch map[string]any) error) error {
	iter := s.session.Query(cql, binds...).PageSize(s.pageSize).IterContext(ctx)
	page := make(map[string]any, s.pageSize)
	row := map[string]any{}
	for iter.MapScan(row) {
		page[KeyOf(s.meta, row)] = normalizeRow(row, s.decimal)
		if len(page) >= s.pageSize {
			if err := fn(page); err != nil {
				_ = iter.Close()
				return err
			}
			page = make(map[string]any, s.pageSize)
		}
		row = map[string]any{}
	}
	if err := iter.Close(); err != nil {
		return fmt.Errorf("cassandra scan: %w", err)
	}
	if len(page) > 0 {
		return fn(page)
	}
	return nil
}

// normalizeRow normalizes every column value of a row, preserving column names.
func normalizeRow(row map[string]any, dec numfmt.DecimalMode) map[string]any {
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[k] = Normalize(v, dec)
	}
	return out
}

// Query runs a raw CQL statement. The forwarded args are joined back into one
// statement (the CLI splits a command line into operands), executed bound to ctx,
// and any result rows are normalized. A statement with no result set (a write, DDL)
// returns an empty list rather than an error.
func (s *Store) Query(ctx context.Context, args []string) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("cassandra: empty statement")
	}
	cql := strings.Join(args, " ")
	iter := s.session.Query(cql).IterContext(ctx)
	rows := []any{}
	row := map[string]any{}
	for iter.MapScan(row) {
		rows = append(rows, normalizeRow(row, s.decimal))
		row = map[string]any{}
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("cassandra: %w", err)
	}
	return rows, nil
}

// FormatRaw renders a raw statement reply as indented JSON, the natural form for
// normalized rows, syntax-highlighted when colored is set.
func (s *Store) FormatRaw(v any, colored bool) string {
	out, err := render.JSON(v, colored)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return out
}

// Close releases the session's connection pool.
func (s *Store) Close() error {
	s.session.Close()
	return nil
}

// tableRef renders the quoted, injection-safe keyspace.table reference used in
// every generated statement.
func (s *Store) tableRef() string {
	return quoteIdent(s.keyspace) + "." + quoteIdent(s.table)
}

// quoteIdent renders a CQL identifier as a double-quoted literal, doubling any
// embedded quote, so a keyspace, table, or column name is interpolated
// injection-safe (identifiers cannot be bound as parameters).
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/render"
)

// scanBatch is the keyset page size for ScanBatches and TypedScan: how many
// nodes to accumulate before handing a page to the caller, bounding streaming
// memory to one page.
const scanBatch = 100

// defaultDatabase is the database a source targets when ?database= is absent; every
// Neo4j deployment has a "neo4j" database by default.
const defaultDatabase = "neo4j"

// errNoLabel is returned when a label-scoped operation (a scan, count, or write)
// runs without a label selected. It is a sentinel so the CLI can surface a clear
// hint. A bounded Get by elementId is allowed without a label.
var errNoLabel = errors.New("neo4j: no label selected; address it as handle.Label or set ?label= in the source url")

// identifier matches a bare Cypher identifier (a label or property name that needs
// no backtick quoting). A source label must satisfy it: this keeps the label
// injection-safe when it is interpolated into a MATCH (labels cannot be query
// parameters) and, by rejecting every marker character, reserves the address
// namespace for the relationship follow-up.
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// targetKind distinguishes the graph entity a collection addresses. v1 only ever
// produces nodeTarget; relTarget is reserved for the relationship-type follow-up so
// its parse branch and Cypher builders slot in without disturbing the node path.
type targetKind int

const nodeTarget targetKind = iota

// target is the resolved collection: which entity, its label (or, later, type), and
// the property whose value is the key ("" means the elementId is the key). Every
// Cypher path is built from a target rather than an inlined MATCH, so the follow-up
// adds a relTarget branch in one place.
type target struct {
	kind  targetKind
	label string
	key   string
}

// match renders the target's MATCH clause. A label-less target (a bounded Get by
// elementId) matches any node; otherwise the validated label is backtick-quoted.
func (t target) match() string {
	if t.label == "" {
		return "MATCH (n)"
	}
	return "MATCH (n:`" + t.label + "`)"
}

// Store adapts one Neo4j database and node label to the query ports. The jq and
// write paths are scoped to the label (the keyspace); the raw path runs arbitrary
// parameterized Cypher and the inspect path reads server and schema metadata.
type Store struct {
	driver                neo4j.DriverWithContext
	database              string
	target                target
	decimal               numfmt.DecimalMode
	trace                 io.Writer
	keyBackedByConstraint bool
}

// connConfig is the parsed form of a neo4j:// source URL: the bolt DSN the driver
// dials (scheme + host only), its basic-auth credentials, the database, and the
// collection target.
type connConfig struct {
	dsn      string
	user     string
	pass     string
	hasAuth  bool
	database string
	target   target
}

// Open connects to the Neo4j server named by a neo4j:// (or bolt://, and their +s /
// +ssc TLS variants) URL and verifies the connection so a bad URL, unreachable
// server, or bad credentials fails fast. The label (the jq keyspace, may be empty
// for a raw/inspect-only or elementId-Get use) is the dotted address override when
// non-empty, else the URL's ?label=; the key property is ?key= (empty means the
// elementId is the key). When trace is non-nil, each Cypher statement the driver
// runs is logged to it (the CLI's --verbose trace) without its parameters, so no
// stored value is ever written. dec chooses how fractional numbers are presented.
func Open(ctx context.Context, rawURL, address string, trace io.Writer, dec numfmt.DecimalMode) (*Store, error) {
	cc, err := parseURL(rawURL, address)
	if err != nil {
		return nil, err
	}
	auth := neo4j.NoAuth()
	if cc.hasAuth {
		auth = neo4j.BasicAuth(cc.user, cc.pass, "")
	}
	drv, err := neo4j.NewDriverWithContext(cc.dsn, auth)
	if err != nil {
		return nil, fmt.Errorf("connect neo4j: %w", err)
	}
	if err := drv.VerifyConnectivity(ctx); err != nil {
		_ = drv.Close(ctx)
		return nil, fmt.Errorf("connect neo4j: %w", err)
	}
	s := &Store{
		driver:   drv,
		database: cc.database,
		target:   cc.target,
		decimal:  dec,
		trace:    trace,
	}
	// A ?key= source upserts by MERGE on that property, which is only well-defined
	// when the property is unique; record whether a uniqueness constraint backs it so
	// the write path can refuse an unsafe fan-out (Put) up front.
	if s.target.key != "" {
		ok, err := s.keyConstraintExists(ctx)
		if err != nil {
			_ = drv.Close(ctx)
			return nil, err
		}
		s.keyBackedByConstraint = ok
	}
	return s, nil
}

// parseURL splits a neo4j:// source URL into the bolt DSN, credentials, database,
// and collection target. The scheme is passed through unchanged (the driver maps
// neo4j/bolt and their +s/+ssc TLS variants); the DSN keeps only scheme + host so
// no credential or query rides in it. A missing scheme or host, an unknown scheme,
// or a label that is not a bare identifier is an error so a malformed source fails
// at parse time.
func parseURL(rawURL, address string) (connConfig, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return connConfig{}, fmt.Errorf("parse neo4j url: %w", err)
	}
	switch u.Scheme {
	case "neo4j", "neo4j+s", "neo4j+ssc", "bolt", "bolt+s", "bolt+ssc":
	default:
		return connConfig{}, fmt.Errorf("neo4j url must use neo4j:// or bolt:// (optionally +s/+ssc), got %q", u.Scheme)
	}
	if u.Host == "" {
		return connConfig{}, fmt.Errorf("neo4j url must name a host, e.g. neo4j://localhost:7687/?label=Person")
	}

	q := u.Query()
	database := q.Get("database")
	if database == "" {
		database = defaultDatabase
	}
	label := address
	if label == "" {
		label = q.Get("label")
	}
	if label != "" && !identifier.MatchString(label) {
		return connConfig{}, fmt.Errorf("neo4j label %q must be a bare identifier (letters, digits, underscore; no leading digit)", label)
	}
	key := q.Get("key")
	if key != "" && !identifier.MatchString(key) {
		return connConfig{}, fmt.Errorf("neo4j key property %q must be a bare identifier", key)
	}

	dsn := url.URL{Scheme: u.Scheme, Host: u.Host}
	cc := connConfig{
		dsn:      dsn.String(),
		database: database,
		target:   target{kind: nodeTarget, label: label, key: key},
	}
	if u.User != nil {
		cc.hasAuth = true
		cc.user = u.User.Username()
		cc.pass, _ = u.User.Password()
	}
	return cc, nil
}

// session opens a session scoped to the store's database in the given access mode.
// Every operation opens and closes its own session; the driver pools connections
// underneath.
func (s *Store) session(ctx context.Context, mode neo4j.AccessMode) neo4j.SessionWithContext {
	return s.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: s.database, AccessMode: mode})
}

// run executes one parameterized Cypher statement, logging the statement (never its
// parameters, which may hold stored values) to the trace writer when set. The error
// is wrapped at our boundary so it anchors here, not deep in the driver.
func (s *Store) run(ctx context.Context, sess neo4j.SessionWithContext, cypher string, params map[string]any) (neo4j.ResultWithContext, error) {
	if s.trace != nil {
		_, _ = fmt.Fprintf(s.trace, "cypher: %s\n", cypher)
	}
	res, err := sess.Run(ctx, cypher, params)
	if err != nil {
		return nil, fmt.Errorf("neo4j run: %w", err)
	}
	return res, nil
}

// Get fetches the nodes whose key matches one of keys and returns them keyed by that
// string. The key is the elementId (default) or the string form of the ?key=
// property. A key with no node maps to nil; empty keys short-circuit. For a ?key=
// source a key matching more than one node is an error — the KV contract is one
// value per key — rather than an arbitrary pick.
func (s *Store) Get(ctx context.Context, keys []string) (map[string]any, error) {
	out := make(map[string]any, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	sess := s.session(ctx, neo4j.AccessModeRead)
	defer func() { _ = sess.Close(ctx) }()

	params := map[string]any{"ids": keys}
	var cypher string
	if s.target.key == "" {
		cypher = s.target.match() + " WHERE elementId(n) IN $ids RETURN elementId(n) AS k, n"
	} else {
		params["key"] = s.target.key
		cypher = s.target.match() + " WHERE toString(n[$key]) IN $ids RETURN toString(n[$key]) AS k, n"
	}
	res, err := s.run(ctx, sess, cypher, params)
	if err != nil {
		return nil, err
	}
	for res.Next(ctx) {
		rec := res.Record()
		k, _ := rec.Values[0].(string)
		node, ok := rec.Values[1].(neo4j.Node)
		if !ok {
			continue
		}
		if _, dup := out[k]; dup && s.target.key != "" {
			return nil, fmt.Errorf("neo4j: key %q matches more than one node; %s.%s is not unique", k, s.target.label, s.target.key)
		}
		out[k] = s.normalizeNode(node)
	}
	if err := res.Err(); err != nil {
		return nil, fmt.Errorf("neo4j get: %w", err)
	}
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			out[k] = nil
		}
	}
	return out, nil
}

// ScanBatches streams the whole label, handing the caller each page of {key: node}.
// It pages by keyset on elementId(n) so a streaming caller holds only
// one page in memory and the order is stable. The key is the ?key= property's string
// form, or the elementId when the property is absent or would collide within a page
// (so a non-unique key never silently drops a node). Bounded by ctx; stops at the
// first error from fn or the driver.
func (s *Store) ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error {
	return s.pagedScan(ctx, "", nil, fn)
}

// pagedScan streams the label through keyset pages ordered by elementId(n),
// optionally narrowed by a WHERE clause and its parameters (the pushdown path). It
// is the shared core of ScanBatches (no filter) and ScanFiltered (a pushed
// predicate). filterParams use f-prefixed names so they never collide with the
// skip/limit/key parameters this method owns.
func (s *Store) pagedScan(ctx context.Context, where string, filterParams map[string]any, fn func(batch map[string]any) error) error {
	if s.target.label == "" {
		return errNoLabel
	}
	sess := s.session(ctx, neo4j.AccessModeRead)
	defer func() { _ = sess.Close(ctx) }()

	keyExpr := "elementId(n)"
	// Keyset pagination: each page fetches the next elementIds after the last one
	// seen, ORDER BY elementId(n). This holds one page in memory, is stable under
	// concurrent writes (unlike SKIP, which can repeat or skip), and needs no page
	// offset arithmetic — the cursor is the last elementId, an empty string first
	// (below every elementId).
	params := map[string]any{"limit": scanBatch, "after": ""}
	if s.target.key != "" {
		params["key"] = s.target.key
		keyExpr = "CASE WHEN n[$key] IS NULL THEN elementId(n) ELSE toString(n[$key]) END"
	}
	for k, v := range filterParams {
		params[k] = v
	}
	cond := "elementId(n) > $after"
	if where != "" {
		cond += " AND (" + where + ")"
	}
	cypher := s.target.match() + " WHERE " + cond + " RETURN " + keyExpr + " AS k, elementId(n) AS eid, n ORDER BY eid LIMIT $limit"

	for {
		res, err := s.run(ctx, sess, cypher, params)
		if err != nil {
			return err
		}
		page := make(map[string]any, scanBatch)
		n := 0
		var lastEid string
		for res.Next(ctx) {
			n++
			rec := res.Record()
			k, _ := rec.Values[0].(string)
			eid, _ := rec.Values[1].(string)
			node, ok := rec.Values[2].(neo4j.Node)
			if !ok {
				continue
			}
			lastEid = eid
			if _, dup := page[k]; dup {
				k = eid // avoid silent in-page loss on a non-unique key.
			}
			page[k] = s.normalizeNode(node)
		}
		if err := res.Err(); err != nil {
			return fmt.Errorf("neo4j scan: %w", err)
		}
		if len(page) > 0 {
			if err := fn(page); err != nil {
				return err
			}
		}
		if n < scanBatch {
			return nil
		}
		params["after"] = lastEid
	}
}

// EstimateCount returns the label's node count, a cheap total for a full scan's
// progress: MATCH (n:Label) RETURN count(n) reads Neo4j's count store rather than
// scanning. It may be stale under concurrent writes, so the caller treats it as a
// hint. A missing label is the same error the scan path returns.
func (s *Store) EstimateCount(ctx context.Context) (int64, error) {
	if s.target.label == "" {
		return 0, errNoLabel
	}
	sess := s.session(ctx, neo4j.AccessModeRead)
	defer func() { _ = sess.Close(ctx) }()

	res, err := s.run(ctx, sess, s.target.match()+" RETURN count(n) AS c", nil)
	if err != nil {
		return 0, err
	}
	rec, err := res.Single(ctx)
	if err != nil {
		return 0, fmt.Errorf("neo4j count: %w", err)
	}
	c, _ := rec.Values[0].(int64)
	return c, nil
}

// Query runs a raw, parameterized Cypher statement. args is the Cypher text and an
// optional JSON object of parameters (never string-built into the statement). It
// returns the result rows as a list of {column: value} maps with every value
// normalized, the natural shape for `iq exec`.
func (s *Store) Query(ctx context.Context, args []string) (any, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, fmt.Errorf("neo4j raw expects a Cypher statement and an optional JSON parameters object")
	}
	cypher := strings.TrimSpace(args[0])
	if cypher == "" {
		return nil, fmt.Errorf("neo4j raw expects a non-empty Cypher statement")
	}
	var params map[string]any
	if len(args) == 2 {
		if err := json.Unmarshal([]byte(args[1]), &params); err != nil {
			return nil, fmt.Errorf("parse neo4j query parameters: %w", err)
		}
	}
	sess := s.session(ctx, neo4j.AccessModeWrite)
	defer func() { _ = sess.Close(ctx) }()

	res, err := s.run(ctx, sess, cypher, params)
	if err != nil {
		return nil, err
	}
	rows := []any{}
	for res.Next(ctx) {
		rec := res.Record()
		row := make(map[string]any, len(rec.Keys))
		for i, k := range rec.Keys {
			row[k] = s.normalizeValue(rec.Values[i])
		}
		rows = append(rows, row)
	}
	if err := res.Err(); err != nil {
		return nil, fmt.Errorf("neo4j query: %w", err)
	}
	return rows, nil
}

// keyConstraintExists reports whether a uniqueness (or node-key) constraint covers
// the target label's key property, the guarantee a ?key= MERGE upsert relies on. It
// reads schema metadata (SHOW CONSTRAINTS), not data.
func (s *Store) keyConstraintExists(ctx context.Context) (bool, error) {
	sess := s.session(ctx, neo4j.AccessModeRead)
	defer func() { _ = sess.Close(ctx) }()

	res, err := s.run(ctx, sess, "SHOW CONSTRAINTS YIELD labelsOrTypes, properties, type", nil)
	if err != nil {
		return false, err
	}
	for res.Next(ctx) {
		rec := res.Record()
		typ, _ := rec.Values[2].(string)
		if !uniquenessConstraint(typ) {
			continue
		}
		if anyContains(rec.Values[0], s.target.label) && anyIsSingleton(rec.Values[1], s.target.key) {
			return true, nil
		}
	}
	if err := res.Err(); err != nil {
		return false, fmt.Errorf("neo4j show constraints: %w", err)
	}
	return false, nil
}

// uniquenessConstraint reports whether a SHOW CONSTRAINTS type guarantees a
// property is unique — the guarantee a ?key= MERGE upsert relies on. UNIQUENESS is
// a plain uniqueness constraint; NODE_KEY (a Neo4j Enterprise node-key constraint)
// also enforces uniqueness (plus presence), so it counts too.
func uniquenessConstraint(typ string) bool {
	return typ == "UNIQUENESS" || typ == "NODE_KEY"
}

// anyContains reports whether list (a driver []any of strings) holds want.
func anyContains(list any, want string) bool {
	items, ok := list.([]any)
	if !ok {
		return false
	}
	for _, it := range items {
		if s, ok := it.(string); ok && s == want {
			return true
		}
	}
	return false
}

// anyIsSingleton reports whether list (a driver []any of strings) is exactly [want]
// — a constraint on the single key property, not a composite that merely includes it.
func anyIsSingleton(list any, want string) bool {
	items, ok := list.([]any)
	if !ok || len(items) != 1 {
		return false
	}
	s, ok := items[0].(string)
	return ok && s == want
}

// FormatRaw renders a raw Cypher reply as indented JSON, the natural form for a
// result set, syntax-highlighted when colored is set.
func (s *Store) FormatRaw(v any, colored bool) string {
	out, err := render.JSON(v, colored)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return out
}

// Close releases the driver and its pooled connections.
func (s *Store) Close() error {
	return s.driver.Close(context.Background())
}

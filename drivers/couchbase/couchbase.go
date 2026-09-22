// Package couchbase adapts a Couchbase collection to the query ports. A collection
// is modelled as Map[key, document]: a document's ID is the key, the JSON document
// is the value. The jq filter runs client-side over documents fetched by ID (a KV
// get) or streamed from the collection (a SQL++ keyset scan), so the semantics match
// every other backend behind the KV port.
//
// A Couchbase cluster nests bucket → scope → collection. A source names the cluster
// and its bucket (?bucket=, required for keyspace work); the collection rides as
// ?collection= or the handle.<[scope.]collection> dotted override, defaulting to
// _default._default. Switching buckets is a different source. Credentials travel in
// the URL userinfo (SDK PasswordAuthenticator) and never in a statement, so the
// CLI's keyring support applies unchanged and the --verbose trace logs statements
// and op names with parameter placeholders only, never a value or a credential.
package couchbase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"strings"
	"time"

	"github.com/couchbase/gocb/v2"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/rawpred"
	"github.com/zsltg/iq/internal/render"
)

// scanBatch is the default page size for the SQL++ keyset scan (ScanBatches,
// ScanFiltered, TypedScan): how many documents to fetch per page, bounding streaming
// memory and the keyset loop's step.
const scanBatch = 100

// connectTimeout bounds Open's readiness probe when the caller's context carries no
// deadline, so a bad URL or unreachable cluster still fails fast rather than hanging.
const connectTimeout = 15 * time.Second

// defaultScope and defaultCollection are Couchbase's always-present default keyspace,
// used when the source selects a bucket but no explicit collection.
const defaultScope, defaultCollection = "_default", "_default"

// maxIdentBytes and maxKeyBytes are Couchbase's documented limits: a keyspace
// identifier (bucket/scope/collection) is at most 251 bytes, a document key at most
// 250. Both are validated at entry so a malformed name fails before any network call.
const maxIdentBytes, maxKeyBytes = 251, 250

// queryNoIndex is the SQL++ error code the query service returns when a scan finds no
// usable index (code 4000). It is mapped to an actionable CREATE PRIMARY INDEX hint,
// since on 7.6+ sequential scan answers index-free queries and the code only surfaces
// where creating an index is the fix.
const queryNoIndex = 4000

// errNoBucket is returned when a keyspace-scoped operation runs without a bucket
// selected. It is a sentinel so the CLI can surface a clear hint.
var errNoBucket = errors.New("couchbase: no bucket selected; set ?bucket= in the source url or address it as handle.collection")

// Store adapts one Couchbase collection to the query ports. The jq and write paths are
// scoped to a single bucket.scope.collection (the keyspace); the raw path runs an
// arbitrary SQL++ statement and the inspect path reads cluster- and bucket-level
// metadata. collection is nil when no bucket is selected (raw/inspect-only use).
type Store struct {
	cluster    *gocb.Cluster
	collection *gocb.Collection
	bucket     string
	scope      string
	coll       string
	pageSize   int
	decimal    numfmt.DecimalMode
	trace      io.Writer
	// prefilterChecked counts rows the client-side raw-byte prefilter evaluated (ran
	// rawpred over) across this store's filtered scans, and prefilterSkipped counts the
	// subset it dropped before decode. They are diagnostic counters the package's tests
	// read to prove the prefilter engages exactly when it should — checked stays zero on
	// an exact-push scan and rises on a prefiltered one — and never part of the public
	// API; no result depends on either.
	prefilterChecked int
	prefilterSkipped int
}

// connConfig is the parsed form of a couchbase:// source URL: the connection string
// gocb dials (iq-owned params and userinfo stripped), the credentials, and the
// selected keyspace. bucket is empty for a raw/inspect-only source.
type connConfig struct {
	connStr  string
	username string
	password string
	bucket   string
	scope    string
	coll     string
}

// Open connects to the Couchbase cluster named by a couchbase:// (or couchbases://)
// URL and verifies the connection with a bounded readiness probe so a bad URL,
// unreachable cluster, or bad credentials fails fast. The bucket (the keyspace, may be
// empty for raw/inspect-only use) comes from ?bucket=; the collection is the dotted
// address override when non-empty, else ?collection=, else _default._default.
// Application telemetry is disabled explicitly. When trace is non-nil, each statement
// and op the driver issues is logged to it (the CLI's --verbose trace) with parameter
// placeholders only, so no value or credential is ever written. dec chooses how
// fractional numbers are presented to the filter.
func Open(ctx context.Context, rawURL, address string, trace io.Writer, dec numfmt.DecimalMode) (*Store, error) {
	cc, err := parseURL(rawURL, address)
	if err != nil {
		return nil, err
	}
	cluster, err := gocb.Connect(cc.connStr, clusterOptions(cc))
	if err != nil {
		return nil, fmt.Errorf("connect couchbase: %w", err)
	}

	// WaitUntilReady honours the shorter of connectTimeout and the caller's context
	// deadline, so a bad URL or unreachable cluster fails fast without a manual clamp.
	if err := cluster.WaitUntilReady(connectTimeout, &gocb.WaitUntilReadyOptions{Context: ctx}); err != nil {
		_ = cluster.Close(nil)
		return nil, fmt.Errorf("connect couchbase: %w", err)
	}

	st := &Store{
		cluster:  cluster,
		bucket:   cc.bucket,
		scope:    cc.scope,
		coll:     cc.coll,
		pageSize: scanBatch,
		decimal:  dec,
		trace:    trace,
	}
	if cc.bucket != "" {
		bucket := cluster.Bucket(cc.bucket)
		if err := bucket.WaitUntilReady(connectTimeout, &gocb.WaitUntilReadyOptions{Context: ctx}); err != nil {
			_ = cluster.Close(nil)
			return nil, fmt.Errorf("connect couchbase: %w", err)
		}
		st.collection = bucket.Scope(cc.scope).Collection(cc.coll)
	}
	return st, nil
}

// clusterOptions builds the SDK options a parsed source connects with: the password
// authenticator from the URL userinfo, and application telemetry off. App telemetry
// reports SDK metrics to the connected cluster by default since gocb v2.10. A read-only
// data tool must not emit it, and the zero value of the config leaves it on, so the flag
// is set explicitly here and asserted by a test.
func clusterOptions(cc connConfig) gocb.ClusterOptions {
	return gocb.ClusterOptions{
		Authenticator:      gocb.PasswordAuthenticator{Username: cc.username, Password: cc.password},
		AppTelemetryConfig: gocb.AppTelemetryConfig{Disabled: true},
	}
}

// parseURL splits a couchbase:// source URL into the gocb connection string, the
// credentials, and the selected keyspace. It strips the iq-owned ?bucket= and
// ?collection= params and the userinfo (which becomes the authenticator), leaving any
// remaining query as gocb connection-string options. The collection is the dotted
// address override, else ?collection=, else _default._default; the scope defaults to
// _default. A missing scheme or host, or a malformed keyspace identifier, is an error
// so a bad source fails at parse time.
func parseURL(rawURL, address string) (connConfig, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return connConfig{}, fmt.Errorf("parse couchbase url: %w", err)
	}
	switch u.Scheme {
	case "couchbase", "couchbases":
	default:
		return connConfig{}, fmt.Errorf("couchbase url must use couchbase:// or couchbases://, got %q", u.Scheme)
	}
	if u.Host == "" {
		return connConfig{}, fmt.Errorf("couchbase url must name a host, e.g. couchbase://user:pass@localhost/?bucket=mybucket")
	}

	q := u.Query()
	bucket := q.Get("bucket")
	if bucket != "" {
		if err := validateIdent("bucket", bucket); err != nil {
			return connConfig{}, err
		}
	}
	collSpec := address
	if collSpec == "" {
		collSpec = q.Get("collection")
	}
	scope, coll, err := parseCollSpec(collSpec)
	if err != nil {
		return connConfig{}, err
	}

	// The connection string gocb dials is the cluster address with iq-owned params and
	// userinfo removed; any remaining query stays as gocb connstr options.
	q.Del("bucket")
	q.Del("collection")
	dial := url.URL{Scheme: u.Scheme, Host: u.Host, RawQuery: q.Encode()}
	password, _ := u.User.Password()
	return connConfig{
		connStr:  dial.String(),
		username: u.User.Username(),
		password: password,
		bucket:   bucket,
		scope:    scope,
		coll:     coll,
	}, nil
}

// parseCollSpec parses a [scope.]collection address into its scope and collection,
// defaulting an empty spec to _default._default and a bare name to the _default scope.
// A spec with more than one dot names no valid keyspace within a bucket and is an error.
func parseCollSpec(spec string) (scope, coll string, err error) {
	if spec == "" {
		return defaultScope, defaultCollection, nil
	}
	parts := strings.Split(spec, ".")
	switch len(parts) {
	case 1:
		scope, coll = defaultScope, parts[0]
	case 2:
		scope, coll = parts[0], parts[1]
	default:
		return "", "", fmt.Errorf("couchbase collection address %q must be collection or scope.collection", spec)
	}
	if err := validateIdent("scope", scope); err != nil {
		return "", "", err
	}
	if err := validateIdent("collection", coll); err != nil {
		return "", "", err
	}
	return scope, coll, nil
}

// validateIdent rejects a keyspace identifier that is empty, over the length limit, or
// carries a character outside Couchbase's documented set. Identifiers cannot be
// parameterized, so this validation plus backtick-quoting (keyspaceRef) is the
// injection guard: a name that survives cannot break out of its backticks.
func validateIdent(kind, name string) error {
	if name == "" {
		return fmt.Errorf("couchbase %s name is empty", kind)
	}
	if len(name) > maxIdentBytes {
		return fmt.Errorf("couchbase %s name exceeds %d bytes", kind, maxIdentBytes)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '%' || r == '.':
		default:
			return fmt.Errorf("couchbase %s name %q has an invalid character", kind, name)
		}
	}
	return nil
}

// validateKey rejects a document key that is empty or over Couchbase's 250-byte limit,
// before any network call. A key rides as a KV op ID or a named parameter, never
// interpolated into a statement, so length and non-emptiness are the whole contract.
func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("couchbase document key is empty")
	}
	if len(key) > maxKeyBytes {
		return fmt.Errorf("couchbase document key exceeds %d bytes", maxKeyBytes)
	}
	return nil
}

// keyspaceRef renders the selected keyspace as a backtick-quoted SQL++ reference
// (`bucket`.`scope`.`collection`). Every identifier was validated at parse time to
// carry no backtick, so quoting is a safe, complete escape.
func (s *Store) keyspaceRef() string {
	return fmt.Sprintf("`%s`.`%s`.`%s`", s.bucket, s.scope, s.coll)
}

// bulkDo runs a KV batch and refuses one the caller has already cancelled. gocb's core
// KV provider reads BulkOpOptions.Timeout only and ignores BulkOpOptions.Context, which
// the SDK marks UNCOMMITTED and honours on the protostellar path alone. A cancelled
// context must therefore stop the batch here, or the read or the write completes and
// reports no error. The SDK's own KV timeout still bounds the batch itself, and this code
// does not override it: the caller's deadline is the longer of the two in normal use, so
// converting it would loosen the bound rather than tighten it. Context stays set because
// it is correct for a couchbase2:// connection.
func (s *Store) bulkDo(ctx context.Context, ops []gocb.BulkOp, tc gocb.Transcoder) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.collection.Do(ops, &gocb.BulkOpOptions{Transcoder: tc, Context: ctx})
}

// Get fetches the documents whose ID matches one of keys and returns them keyed by ID.
// A key with no document is absent from the map, per the KV contract. A non-JSON (binary)
// document is rendered as a string. Empty keys short-circuit with no round-trip.
func (s *Store) Get(ctx context.Context, keys []string) (map[string]any, error) {
	if s.collection == nil {
		return nil, errNoBucket
	}
	out := make(map[string]any, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	ops := make([]gocb.BulkOp, 0, len(keys))
	getOps := make([]*gocb.GetOp, 0, len(keys))
	for _, k := range keys {
		if err := validateKey(k); err != nil {
			return nil, err
		}
		op := &gocb.GetOp{ID: k}
		getOps = append(getOps, op)
		ops = append(ops, op)
	}
	s.tracef("get %s", strings.Join(keys, " "))
	if err := s.bulkDo(ctx, ops, rawTranscoder{}); err != nil {
		return nil, fmt.Errorf("couchbase get: %w", err)
	}
	for _, op := range getOps {
		key := op.ID
		if op.Err != nil {
			if errors.Is(op.Err, gocb.ErrDocumentNotFound) {
				continue
			}
			return nil, fmt.Errorf("couchbase get: %w", op.Err)
		}
		var raw []byte
		if err := op.Result.Content(&raw); err != nil {
			return nil, fmt.Errorf("couchbase get: decode %q: %w", key, err)
		}
		out[key] = decodeValue(raw, s.decimal)
	}
	return out, nil
}

// ScanBatches streams the whole collection, handing the caller each page of
// {id: document}. It walks a SQL++ keyset scan ordered by document ID — never
// OFFSET/LIMIT paging — following the last ID of each page as the cursor, so a
// streaming caller keeps only one page in memory and the loop provably terminates on a
// short page. Bounded by ctx; stops at the first error from fn or the driver.
func (s *Store) ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error {
	return s.scan(ctx, "", nil, nil, fn)
}

// scan runs the keyset walk shared by ScanBatches and ScanFiltered. where, when
// non-empty, is an extra predicate ANDed into each page's WHERE (its named parameters
// ride in params); it must never reference the reserved $after/$page names. Every
// value travels as a named parameter, never concatenated.
//
// When matcher is non-nil, each row's raw value is run through it before decode, and a
// row the prepared matcher proves the predicate rejects is skipped (counted in
// prefilterSkipped) rather than decoded and delivered — a byte-level drop that never
// changes results because the matcher's predicate is a conservative superset the caller
// re-runs in full. The keyset cursor advances past every row, skipped or kept, so
// pagination never re-reads or loops; a page emptied entirely by the prefilter is never
// handed to fn, preserving the "a scan never yields an empty batch" contract.
func (s *Store) scan(ctx context.Context, where string, params map[string]any, matcher *rawpred.Matcher, fn func(batch map[string]any) error) error {
	if s.collection == nil {
		return errNoBucket
	}
	ref := s.keyspaceRef()
	clause := "META(t).id > $after"
	if where != "" {
		clause += " AND (" + where + ")"
	}
	stmt := fmt.Sprintf(
		"SELECT META(t).id AS k, t AS v FROM %s t WHERE %s ORDER BY META(t).id LIMIT $page",
		ref, clause,
	)
	after := ""
	for {
		args := map[string]any{"after": after, "page": s.pageSize}
		maps.Copy(args, params)
		s.tracef("query %s", stmt)
		rows, err := s.query(ctx, stmt, args)
		if err != nil {
			return err
		}
		page := make(map[string]any, s.pageSize)
		last := ""
		n := 0
		for rows.Next() {
			var row struct {
				K string          `json:"k"`
				V json.RawMessage `json:"v"`
			}
			if err := rows.Row(&row); err != nil {
				_ = rows.Close()
				return fmt.Errorf("couchbase scan: %w", err)
			}
			// Advance the keyset cursor for every row read, dropped or kept, so a
			// prefiltered-out document never stalls or rewinds the walk.
			n++
			last = row.K
			if matcher != nil {
				s.prefilterChecked++
				if matcher.Match(row.V) == rawpred.CannotMatch {
					s.prefilterSkipped++
					continue
				}
			}
			page[row.K] = decodeValue(row.V, s.decimal)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return s.queryError("couchbase scan", err)
		}
		_ = rows.Close()

		if len(page) > 0 {
			if err := fn(page); err != nil {
				return err
			}
		}
		// A page shorter than the limit is the last one; otherwise advance the cursor
		// past the last ID read. IDs are unique and strictly increasing, so the walk
		// makes progress and terminates.
		if n < s.pageSize {
			return nil
		}
		after = last
	}
}

// Query runs a raw SQL++ statement against the selected scope (or the cluster when no
// bucket is selected). args[0] is the statement; an optional args[1] is a JSON object
// of named parameters, bound end-to-end — never string-built. Rows are returned
// normalized, in order.
func (s *Store) Query(ctx context.Context, args []string) (any, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, fmt.Errorf("couchbase raw expects a SQL++ statement and an optional JSON named-parameters object")
	}
	stmt := args[0]
	var params map[string]any
	// args[1:] is the optional parameters object (at most one, per the guard above);
	// ranging avoids an index the SAST cannot prove in bounds.
	for _, raw := range args[1:] {
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			return nil, fmt.Errorf("parse couchbase query parameters: %w", err)
		}
	}
	s.tracef("query %s", stmt)
	rows, err := s.query(ctx, stmt, params)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []any{}
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Row(&raw); err != nil {
			return nil, fmt.Errorf("couchbase query: %w", err)
		}
		out = append(out, decodeValue(raw, s.decimal))
	}
	if err := rows.Err(); err != nil {
		return nil, s.queryError("couchbase query", err)
	}
	return out, nil
}

// query runs one SQL++ statement with named parameters, scope-qualified when a bucket
// is selected so unqualified keyspace names resolve. Scan consistency is RequestPlus
// so a scan reads the tool's own just-written documents (read-your-writes), the
// behaviour a data-inspection tool needs.
func (s *Store) query(ctx context.Context, stmt string, params map[string]any) (*gocb.QueryResult, error) {
	opts := &gocb.QueryOptions{
		NamedParameters: params,
		ScanConsistency: gocb.QueryScanConsistencyRequestPlus,
		Context:         ctx,
	}
	if s.bucket != "" {
		return s.cluster.Bucket(s.bucket).Scope(s.scope).Query(stmt, opts)
	}
	return s.cluster.Query(stmt, opts)
}

// queryError wraps a SQL++ error at the boundary, upgrading the "no index available"
// case (code 4000) to an actionable CREATE PRIMARY INDEX hint. A raw gocb error dump
// is never surfaced to the user.
func (s *Store) queryError(op string, err error) error {
	if qerr, ok := errors.AsType[*gocb.QueryError](err); ok {
		for _, d := range qerr.Errors {
			if d.Code == queryNoIndex {
				return fmt.Errorf(
					"%s: no index available for the scan; create a primary index, e.g. CREATE PRIMARY INDEX ON %s: %w",
					op, s.keyspaceRef(), err,
				)
			}
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

// FormatRaw renders a raw SQL++ reply as indented JSON, the natural form for a
// document store, syntax-highlighted when colored is set.
func (s *Store) FormatRaw(v any, colored bool) string {
	out, err := render.JSON(v, colored)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return out
}

// Close releases the cluster's resources.
func (s *Store) Close() error {
	if err := s.cluster.Close(nil); err != nil {
		return fmt.Errorf("close couchbase: %w", err)
	}
	return nil
}

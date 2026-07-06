package couchdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/go-kivik/kivik/v4"
	_ "github.com/go-kivik/kivik/v4/couchdb" // registers the "couch" driver.

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/render"
)

// scanBatch is the default page size for ScanBatches and TypedScan: how many
// documents to accumulate before handing a page to the caller, bounding streaming
// memory. It is also the _find page size for ScanFiltered.
const scanBatch = 100

// designPrefix marks CouchDB design documents (views, Mango indexes). They are
// database metadata, not user data, so scans skip them — the KV model exposes only
// documents.
const designPrefix = "_design/"

// errNoDatabase is returned when a database-scoped operation runs without a
// database selected. It is a sentinel so the CLI can surface a clear hint.
var errNoDatabase = errors.New("couchdb: no database selected; address it as handle.database or set ?database= in the source url")

// Store adapts one CouchDB database to the query ports. The jq and write paths are
// scoped to a single database (the keyspace); the raw path runs a Mango _find and
// the inspect path reads server- and database-level metadata.
type Store struct {
	client   *kivik.Client
	db       string
	pageSize int
	decimal  numfmt.DecimalMode
}

// connConfig is the parsed form of a couchdb:// source URL: the server DSN kivik
// connects to (an http(s):// URL, credentials preserved) and the default database.
type connConfig struct {
	dsn string
	db  string
}

// Open connects to the CouchDB server named by a couchdb:// (or couchdbs://) URL
// and verifies the connection with a ping so a bad URL, unreachable server, or bad
// credentials fails fast. The database (the jq keyspace, may be empty for
// raw/inspect-only use) is the dotted address override when non-empty, else the
// URL's ?database= default, else the URL path. When trace is non-nil, each HTTP
// request the driver issues is logged to it (the CLI's --verbose trace) as
// method+path only, so no credential (carried in the Authorization header) is ever
// written. dec chooses how fractional numbers are presented to the filter.
func Open(ctx context.Context, rawURL, address string, trace io.Writer, dec numfmt.DecimalMode) (*Store, error) {
	cc, err := parseURL(rawURL, address)
	if err != nil {
		return nil, err
	}
	opts := []kivik.Option{}
	if trace != nil {
		opts = append(opts, traceOption(trace))
	}
	client, err := kivik.New("couch", cc.dsn, opts...)
	if err != nil {
		return nil, fmt.Errorf("connect couchdb: %w", err)
	}
	if _, err := client.Ping(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect couchdb: %w", err)
	}
	return &Store{
		client:   client,
		db:       cc.db,
		pageSize: scanBatch,
		decimal:  dec,
	}, nil
}

// parseURL splits a couchdb:// source URL into the server DSN and the default
// database. couchdb:// maps to an http:// DSN and couchdbs:// to https://; the
// database is the dotted address override, else ?database=, else the URL path. A
// missing scheme or host is an error so a malformed source fails at parse time.
func parseURL(rawURL, address string) (connConfig, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return connConfig{}, fmt.Errorf("parse couchdb url: %w", err)
	}
	var httpScheme string
	switch u.Scheme {
	case "couchdb":
		httpScheme = "http"
	case "couchdbs":
		httpScheme = "https"
	default:
		return connConfig{}, fmt.Errorf("couchdb url must use couchdb:// or couchdbs://, got %q", u.Scheme)
	}
	if u.Host == "" {
		return connConfig{}, fmt.Errorf("couchdb url must name a host, e.g. couchdb://localhost:5984/?database=mydb")
	}

	q := u.Query()
	db := address
	if db == "" {
		db = q.Get("database")
	}
	if db == "" {
		// Lenient: accept the database in the path too (couchdb://host/mydb), the
		// idiomatic CouchDB REST form, as long as it names a single database.
		if p := strings.Trim(u.Path, "/"); p != "" && !strings.Contains(p, "/") {
			db = p
		}
	}

	// The DSN kivik connects to is the server root: scheme + userinfo + host, no
	// path or query (the database is selected per call with client.DB).
	dsn := url.URL{Scheme: httpScheme, User: u.User, Host: u.Host, Path: "/"}
	return connConfig{dsn: dsn.String(), db: db}, nil
}

// Get fetches the documents whose _id matches one of keys and returns them keyed by
// _id string. A key with no document (missing or deleted) maps to nil. Empty keys
// short-circuit with no round-trip.
func (s *Store) Get(ctx context.Context, keys []string) (map[string]any, error) {
	if s.db == "" {
		return nil, errNoDatabase
	}
	out := make(map[string]any, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	rows := s.client.DB(s.db).AllDocs(ctx, kivik.IncludeDocs(), kivik.Param("keys", keys))
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw json.RawMessage
		// A missing or deleted key has no document body; ScanDoc reports it, and the
		// key is filled with nil below, matching the KV contract.
		if err := rows.ScanDoc(&raw); err != nil {
			continue
		}
		doc, err := decodeDoc(raw, s.decimal)
		if err != nil {
			return nil, err
		}
		id, err := rows.ID()
		if err != nil {
			return nil, fmt.Errorf("couchdb row id: %w", err)
		}
		out[id] = doc
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("couchdb all_docs: %w", err)
	}
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			out[k] = nil
		}
	}
	return out, nil
}

// ScanBatches streams the whole database, handing the caller each page of
// {_id: document}. A single _all_docs request streams every row from the server;
// the driver accumulates them into pages of pageSize so a streaming caller keeps
// only one page in memory. Design documents are skipped. Bounded by ctx; stops at
// the first error from fn or the driver.
func (s *Store) ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error {
	if s.db == "" {
		return errNoDatabase
	}
	rows := s.client.DB(s.db).AllDocs(ctx, kivik.IncludeDocs())
	defer func() { _ = rows.Close() }()

	page := make(map[string]any, s.pageSize)
	for rows.Next() {
		id, err := rows.ID()
		if err != nil {
			return fmt.Errorf("couchdb row id: %w", err)
		}
		if strings.HasPrefix(id, designPrefix) {
			continue
		}
		var raw json.RawMessage
		if err := rows.ScanDoc(&raw); err != nil {
			return fmt.Errorf("couchdb scan document: %w", err)
		}
		doc, err := decodeDoc(raw, s.decimal)
		if err != nil {
			return err
		}
		page[id] = doc
		if len(page) >= s.pageSize {
			if err := fn(page); err != nil {
				return err
			}
			page = make(map[string]any, s.pageSize)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("couchdb all_docs: %w", err)
	}
	if len(page) > 0 {
		return fn(page)
	}
	return nil
}

// EstimateCount returns the database's document count from its metadata (the
// doc_count in GET /{db}), a cheap approximate total for a full scan's progress. It
// counts design documents too and may be stale under concurrent writes, so the
// caller treats it as a hint. A missing database is the same error the scan paths
// return.
func (s *Store) EstimateCount(ctx context.Context) (int64, error) {
	if s.db == "" {
		return 0, errNoDatabase
	}
	stats, err := s.client.DB(s.db).Stats(ctx)
	if err != nil {
		return 0, fmt.Errorf("couchdb db stats: %w", err)
	}
	return stats.DocCount, nil
}

// Query runs a raw Mango query against the database. args must be a single JSON
// document: either a full _find request (`{"selector":{...},"limit":...}`) or a
// bare selector (`{"year":2017}`), which is wrapped as `{"selector":...}`. It is
// decoded and passed as a parameter object — never string-built — and returns the
// matching documents with the paging bookmark.
func (s *Store) Query(ctx context.Context, args []string) (any, error) {
	if s.db == "" {
		return nil, errNoDatabase
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("couchdb raw expects one JSON Mango query document")
	}
	var query map[string]any
	if err := json.Unmarshal([]byte(args[0]), &query); err != nil {
		return nil, fmt.Errorf("parse couchdb mango query: %w", err)
	}
	if _, ok := query["selector"]; !ok {
		query = map[string]any{"selector": query}
	}

	rows := s.client.DB(s.db).Find(ctx, query)
	defer func() { _ = rows.Close() }()
	docs := []any{}
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.ScanDoc(&raw); err != nil {
			return nil, fmt.Errorf("couchdb scan document: %w", err)
		}
		doc, err := decodeDoc(raw, s.decimal)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("couchdb find: %w", err)
	}
	out := map[string]any{"docs": docs}
	if md, err := rows.Metadata(); err == nil && md.Bookmark != "" {
		out["bookmark"] = md.Bookmark
	}
	return out, nil
}

// FormatRaw renders a raw Mango reply as indented JSON, the natural form for a
// document store, syntax-highlighted when colored is set.
func (s *Store) FormatRaw(v any, colored bool) string {
	out, err := render.JSON(v, colored)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return out
}

// Close releases the client's resources.
func (s *Store) Close() error {
	return s.client.Close()
}

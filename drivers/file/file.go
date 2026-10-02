// Package file adapts a local database dump file to the query ports, so a dump is
// queried, inspected, diffed, and copied-from exactly like a live backend. It is
// read-only: it implements the read and typed-scan ports but no writer, so a
// file:// endpoint is never a copy destination. Formats are detected by content
// (or forced by a ?format= URL query): iq's own typed JSONL, mongoexport Extended
// JSON, mongodump BSON, and Redis RDB. The Store holds only the path and reopens
// per operation, so scans stream and no values are held in memory beyond one page;
// whole-dataset queries obey the core's --unbounded contract like any backend.
package file

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// pageSize bounds how many records a scan buffers before handing a page to the
// caller, so a stream holds only one page in memory.
const pageSize = 500

// recordPager hands records to fn in pages of size, reusing one slice. A caller of fn
// must not keep the page.
type recordPager struct {
	page []query.Record
	size int
	fn   func([]query.Record) error
}

// newRecordPager returns a pager for fn.
func newRecordPager(size int, fn func([]query.Record) error) *recordPager {
	return &recordPager{page: make([]query.Record, 0, size), size: size, fn: fn}
}

// add appends r and hands a full page to fn.
func (p *recordPager) add(r query.Record) error {
	p.page = append(p.page, r)
	if len(p.page) < p.size {
		return nil
	}
	if err := p.fn(p.page); err != nil {
		return err
	}
	p.page = p.page[:0]
	return nil
}

// flush hands a non-empty tail to fn.
func (p *recordPager) flush() error {
	if len(p.page) == 0 {
		return nil
	}
	return p.fn(p.page)
}

// ErrRawUnsupported reports that a raw backend command (Redis command, Mongo
// runCommand) has no meaning for a file source. It is a sentinel so inspect/diff
// can skip their server-info sections and still run their scan-based work.
var ErrRawUnsupported = errors.New("raw commands are not available for a file source")

// errStopScan unwinds a filtered scan early once every requested key is found; it
// is caught in Get and never surfaces to the caller.
var errStopScan = errors.New("file: stop scan")

// Store is a read-only view over a dump file.
type Store struct {
	path   string
	data   []byte // when non-nil, the dump is this in-memory buffer (stdin), not path.
	format Format
	dec    numfmt.DecimalMode
	hints  Hints       // schema hints (?types=/?keys=) a native dump omits; empty for self-describing formats.
	cache  CacheConfig // decode-cache policy; zero value disables caching.

	// prefilterChecked counts records the client-side raw-byte prefilter evaluated
	// (ran rawpred over) across this store's filtered scans, and prefilterSkipped
	// counts the subset it dropped before decode. They are test-only observability —
	// never read by a query — proving the prefilter engages exactly when it should:
	// checked stays zero on a bypassed scan (a non-JSONL format, or a fresh CBOR
	// cache) and rises on a prefiltered one. Never part of the public surface.
	prefilterChecked int
	prefilterSkipped int
}

// Hints carries the schema metadata a native dump does not itself record but a reader
// needs to reproduce the live shape: the ?types= column-type map and the ?keys= key
// schema from a file:// URL. Both are the raw query values; each format's reader parses
// what it needs and ignores the rest. Self-describing formats (JSONL, RDB, BSON,
// mongoexport) leave them empty.
type Hints struct {
	Types   string
	Keys    string
	Columns string // explicit column names for a headerless dump (Cassandra COPY without HEADER).
	Label   string // Neo4j: the node label whose nodes a scan exposes (?label=).
	Rel     string // Neo4j: the relationship type whose relationships a scan exposes (?rel=).
	Key     string // Neo4j: the node/relationship property to key records by (?key=); default is the export id.
}

// Open resolves a file:// URL to a read-only dump Store. The URL path is the dump
// file; an optional ?format= (rdb|bson|mongoexport|jsonl|yaml) overrides content
// detection for the ambiguous text formats. cache is the decode-cache policy; a
// zero CacheConfig disables caching, so a caller that wires none keeps the
// always-decode behavior.
func Open(rawURL string, dec numfmt.DecimalMode, cache CacheConfig) (*Store, error) {
	path, forced, hints, err := parseFileURL(rawURL)
	if err != nil {
		return nil, err
	}
	format := forced
	if format == FormatUnknown {
		format, err = detectFormat(path)
		if err != nil {
			return nil, err
		}
	}
	return &Store{path: path, format: format, dec: dec, hints: hints, cache: cache}, nil
}

// OpenReader builds a read-only Store over an in-memory dump buffer — used for
// piped stdin, which is not seekable, so it is read once into data and re-scanned
// from a bytes.Reader per operation. The format is content-sniffed from the buffer
// unless forced.
func OpenReader(data []byte, format Format, dec numfmt.DecimalMode) (*Store, error) {
	if format == FormatUnknown {
		var err error
		format, err = detectBytes(data)
		if err != nil {
			return nil, err
		}
	}
	return &Store{data: data, format: format, dec: dec}, nil
}

// URL returns the file:// URL for a filesystem path, the inverse of DumpPath:
// an absolute Unix path becomes file:///abs/path, a Windows drive path the RFC
// 8089 form file:///C:/dir/file (slash-separated, percent-escaped), which
// parseFileURL folds back to the native path.
func URL(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	if isDrivePath(u.Path) {
		u.Path = "/" + u.Path
	}
	return u.String()
}

// isDrivePath reports whether p starts with a Windows drive letter and colon.
func isDrivePath(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// nativePath maps the path of a parsed file:// URL to the filesystem path for
// goos. On Windows the RFC 8089 drive form file:///C:/dir/file parses to
// /C:/dir/file: the leading slash separates the empty host from the drive, so
// the native path starts at the drive letter. Every other path is returned as
// is.
func nativePath(goos, path string) string {
	if goos != "windows" {
		return path
	}
	rest, ok := strings.CutPrefix(path, "/")
	if !ok || !isDrivePath(rest) {
		return path
	}
	return filepath.FromSlash(rest)
}

// DumpPath returns the filesystem path a file:// URL refers to — the same path
// Open resolves and the decode cache records in its header — so a caller can map
// a saved file source to its cache entry (iq cache clear @src).
func DumpPath(rawURL string) (string, error) {
	path, _, _, err := parseFileURL(rawURL)
	return path, err
}

// DetectFormat reports the dump format a file:// URL resolves to: an explicit
// ?format= wins, otherwise the content is sniffed from the file. Exported so a
// caller (iq ls -v) can name a file source's format without building a Store.
func DetectFormat(rawURL string) (Format, error) {
	path, forced, _, err := parseFileURL(rawURL)
	if err != nil {
		return FormatUnknown, err
	}
	if forced != FormatUnknown {
		return forced, nil
	}
	return detectFormat(path)
}

// parseFileURL splits a file:// URL into its path, an optional forced format from the
// ?format= query, and the ?types=/?keys= schema hints a native dump reader may need. It
// rejects a non-file scheme and an empty path.
func parseFileURL(raw string) (path string, format Format, hints Hints, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", FormatUnknown, Hints{}, fmt.Errorf("parse file url: %w", err)
	}
	if u.Scheme != "file" {
		return "", FormatUnknown, Hints{}, fmt.Errorf("not a file url: %q", raw)
	}
	path = urlPath(u)
	if path == "" {
		return "", FormatUnknown, Hints{}, errors.New("file url has no path")
	}
	path = nativePath(runtime.GOOS, path)
	q := u.Query()
	if f := q.Get("format"); f != "" {
		format, err = ParseFormat(f)
		if err != nil {
			return "", FormatUnknown, Hints{}, err
		}
	}
	return path, format, hintsFrom(q), nil
}

// urlPath returns the path a parsed file:// URL names: its path, else its opaque
// part (the file:relative form), with a host other than localhost folded back in.
func urlPath(u *url.URL) string {
	path := u.Path
	if path == "" {
		path = u.Opaque // file:relative form.
	}
	if u.Host != "" && u.Host != "localhost" {
		// file://segment/... puts the first segment in Host; fold it back so a
		// two-slash relative-looking url still names a path rather than being lost.
		path = u.Host + path
	}
	return path
}

// hintsFrom reads the schema hints from the query of a file:// URL.
func hintsFrom(q url.Values) Hints {
	return Hints{
		Types:   q.Get("types"),
		Keys:    q.Get("keys"),
		Columns: q.Get("columns"),
		Label:   q.Get("label"),
		Rel:     q.Get("rel"),
		Key:     q.Get("key"),
	}
}

// records is the cache-aware decode entry point every read path shares. When a
// fresh decode cache exists it streams that; otherwise it decodes the original
// dump, and when populate is set and the dump is cacheable, tees the decode into
// a new cache installed only on a complete pass. populate is false for Get, whose
// early stop would persist a partial dump — so only a full scan populates.
func (s *Store) records(ctx context.Context, populate bool, fn func(batch []query.Record) error) error {
	if src, ok := s.cacheSource(); ok {
		return src(ctx, fn)
	}
	if populate {
		if m, ok := s.cacheable(); ok {
			return s.populate(ctx, m, fn)
		}
	}
	return s.recordsDirect(ctx, fn)
}

// recordsDirect decodes the dump straight from its bytes, with no cache: it opens
// the dump (a file, reopened per operation, or the in-memory stdin buffer),
// builds the source for its format, and drives it.
func (s *Store) recordsDirect(ctx context.Context, fn func(batch []query.Record) error) error {
	r, closeR, err := s.reader()
	if err != nil {
		return err
	}
	defer func() { _ = closeR() }()
	dr, err := maybeGunzip(r)
	if err != nil {
		return err
	}
	src, err := RecordSourceFor(dr, s.format, s.dec, s.hints)
	if err != nil {
		return err
	}
	return src(ctx, fn)
}

// maybeGunzip transparently unwraps a gzip stream so a compressed dump (a DynamoDB S3
// export ships gzipped NDJSON) decodes like a plain one. It peeks the two-byte gzip
// magic without consuming it; a non-gzip stream passes through untouched.
func maybeGunzip(r io.Reader) (io.Reader, error) {
	br := bufio.NewReader(r)
	magic, err := br.Peek(2)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return br, nil // too short to be gzip; let the decoder report an empty/short dump.
		}
		return nil, fmt.Errorf("read dump head: %w", err)
	}
	if magic[0] != 0x1f || magic[1] != 0x8b {
		return br, nil
	}
	gz, err := gzip.NewReader(br)
	if err != nil {
		return nil, fmt.Errorf("open gzip dump: %w", err)
	}
	return gz, nil
}

// reader yields a fresh reader over the dump: a bytes.Reader over the buffered
// stdin, or a freshly reopened file (so scans and Get each re-read from the start).
func (s *Store) reader() (io.Reader, func() error, error) {
	if s.data != nil {
		return bytes.NewReader(s.data), func() error { return nil }, nil
	}
	f, err := os.Open(s.path) //nolint:gosec // the path is a user-supplied dump file, by design.
	if err != nil {
		return nil, nil, fmt.Errorf("open dump %q: %w", s.path, err)
	}
	return f, f.Close, nil
}

// RecordSourceFor returns the RecordSource that decodes r for a given format. It is
// the one decoder-dispatch shared by the file:// store, the buffered stdin store,
// and the cmd move importer, so every reader honors the same format set.
func RecordSourceFor(r io.Reader, format Format, dec numfmt.DecimalMode, hints Hints) (query.RecordSource, error) {
	switch format {
	case FormatJSONL:
		return query.JSONSource(r, pageSize, false), nil
	case FormatYAML:
		return query.YAMLSource(r, pageSize, false), nil
	case FormatRDB:
		return rdbSource(r, pageSize), nil
	case FormatBSON:
		return bsonSource(r, pageSize, dec), nil
	case FormatMongoexport:
		return extJSONSource(r, pageSize, dec), nil
	case FormatDynamoDBJSON:
		return dynamoSource(r, pageSize, dec, hints)
	case FormatCassandraCSV:
		return cassandraCSVSource(r, pageSize, dec, hints)
	case FormatNeo4jJSON:
		return neo4jSource(r, pageSize, dec, hints)
	default:
		return nil, errors.New("unknown dump format")
	}
}

// TypedScan streams the dump as typed records, so a copy from a file reconstructs
// each item's native structure.
func (s *Store) TypedScan(ctx context.Context, fn func(batch []query.Record) error) error {
	return s.records(ctx, true, fn)
}

// ScanBatches streams the dump as {key: value} pages, dropping the type tag for
// the jq read path.
func (s *Store) ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error {
	return s.records(ctx, true, func(recs []query.Record) error {
		return fn(recordBatch(recs))
	})
}

// Get resolves specific keys with a single filtered streaming pass, stopping once
// every requested key is found. A key not in the dump is absent from the map,
// matching the KVStore contract.
func (s *Store) Get(ctx context.Context, keys []string) (map[string]any, error) {
	if len(keys) == 0 {
		return map[string]any{}, nil
	}
	// A fresh indexed cache resolves the keys by decoding only candidate pages; on
	// any miss it reports false and the streaming path below runs unchanged.
	if out, ok := s.cacheGet(ctx, keys); ok {
		return out, nil
	}
	l := newKeyLookup(keys)
	err := s.records(ctx, false, func(recs []query.Record) error {
		for _, r := range recs {
			if l.take(r) {
				return errStopScan
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopScan) {
		return nil, err
	}
	return l.out, nil
}

// Query rejects raw commands: a file has no server to run one against. The sentinel
// lets inspect/diff degrade gracefully.
func (s *Store) Query(_ context.Context, _ []string) (any, error) {
	return nil, ErrRawUnsupported
}

// Close releases the store. Nothing is held open between operations, so it is a
// no-op, present to satisfy the port.
func (s *Store) Close() error { return nil }

// FormatRaw renders a value as compact JSON. A file source never produces a raw
// reply (Query errors), so this exists only to satisfy the CLI's backend port.
func (s *Store) FormatRaw(v any, _ bool) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// ExplainPlan describes, without opening the file, the work a file query does: a
// full decode then a client-side filter or scan. There is no server-side pushdown.
// When a scan carries a compiled predicate, an uncached typed-JSONL dump additionally
// runs a client-side raw-byte prefilter (rawpred) that drops a provable non-match
// before decode; every other format, and any scan served from a fresh decode cache,
// decodes in full and lets the client filter. The plan is static (it opens no file),
// so it names the condition rather than resolving the dump's format here.
func ExplainPlan(keys selector.KeySet, pred predicate.Node, _ bool) query.AccessPlan {
	if !keys.Scan && len(keys.Keys) > 0 {
		return query.AccessPlan{Ops: []string{
			"decode dump file",
			fmt.Sprintf("filter to %d key(s) client-side", len(keys.Keys)),
		}}
	}
	if pred != nil {
		return query.AccessPlan{Ops: []string{
			"decode dump file",
			"uncached typed-JSONL only: client-side raw-byte prefilter (rawpred) drops a provable non-match before decode",
			"scan client-side",
		}}
	}
	return query.AccessPlan{Ops: []string{"decode dump file", "scan client-side"}}
}

// Package file adapts a local database dump file to the query ports. A dump is
// then queried, inspected, diffed, and copied from like a live backend. It is
// read-only. It implements the read and typed-scan ports and no writer, so a
// file:// endpoint is never a copy destination. Content detects the format. A
// ?format= URI query can force it. The formats are iq's own typed JSONL,
// mongoexport Extended JSON, mongodump BSON, and Redis RDB. The Store holds only
// the path and reopens it per operation. Scans stream, and no more than one page
// of values is in memory. Whole-dataset queries obey the --unbounded contract of
// the core.
package file

import (
	"bufio"
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
	"slices"
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

// errStopScan unwinds a filtered scan early once every requested key is found.
// Get catches it, and it never surfaces to the caller.
var errStopScan = errors.New("file: stop scan")

// Store is a read-only view over a dump file.
type Store struct {
	path   string
	pipe   *pipeInput // when non-nil, the dump is this one-shot stream (stdin), not path.
	format Format
	dec    numfmt.DecimalMode
	hints  Hints       // schema hints (?types=/?keys=) a native dump omits; empty for self-describing formats.
	cache  CacheConfig // decode-cache policy; zero value disables caching.

	// prefilterChecked counts records that the client-side raw-byte prefilter
	// evaluated (ran rawpred over) across this store's filtered scans. The
	// counter prefilterSkipped counts the subset that it dropped before decode.
	// They are test-only observability. A query never reads them. They prove
	// that the prefilter engages in exactly the intended cases. Here checked
	// stays zero on a bypassed scan (a non-JSONL format, or a fresh CBOR cache)
	// and rises on a prefiltered one. They are never part of the public
	// surface.
	prefilterChecked int
	prefilterSkipped int
}

// Hints carries the schema metadata that a native dump does not itself record.
// A reader needs it to reproduce the live shape. It holds the ?types= column-type
// map and the ?keys= key schema from a file:// URI. Both are the raw query
// values. Each format's reader parses what it needs and ignores the rest.
// Self-describing formats (JSONL, RDB, BSON, mongoexport) leave them empty.
type Hints struct {
	Types   string
	Keys    string
	Columns string // explicit column names for a headerless dump (Cassandra COPY without HEADER).
	Label   string // Neo4j: the node label whose nodes a scan exposes (?label=).
	Rel     string // Neo4j: the relationship type whose relationships a scan exposes (?rel=).
	Key     string // Neo4j: the node/relationship property to key records by (?key=); default is the export id.
}

// Open resolves a file:// URI to a read-only dump Store. The URI path is the
// dump file. An optional ?format= (rdb|bson|mongoexport|jsonl|yaml) overrides
// content detection for the ambiguous text formats. The cache argument is the
// decode-cache policy. A zero CacheConfig disables caching, so a caller that
// wires none keeps the always-decode behavior.
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

// ErrPipeReadOnce reports a second read of piped stdin. A pipe cannot rewind,
// and iq does not buffer it, so only the first read sees the data.
var ErrPipeReadOnce = errors.New("piped stdin can be read only once; save it to a file and read the file instead")

// pipeInput is the one-shot stream behind a Store that reads piped stdin.
type pipeInput struct {
	r    *bufio.Reader
	used bool
}

// take hands out the stream on the first call. Every later call fails.
func (p *pipeInput) take() (io.Reader, error) {
	if p.used {
		return nil, ErrPipeReadOnce
	}
	p.used = true
	return p.r, nil
}

// OpenPipe builds a read-only Store over piped stdin. The stream is read once, as
// it comes, and never buffered whole. The Store hands the stream to its first
// read. A second read fails with ErrPipeReadOnce. When format is unknown, OpenPipe
// detects it from the first 512 bytes, as for a file without a name.
func OpenPipe(r io.Reader, format Format, dec numfmt.DecimalMode) (*Store, error) {
	br := bufio.NewReader(r)
	if format == FormatUnknown {
		var err error
		if format, err = detectPeek(br); err != nil {
			return nil, err
		}
	}
	return &Store{pipe: &pipeInput{r: br}, format: format, dec: dec}, nil
}

// URL returns the file:// URI for a filesystem path, the inverse of DumpPath.
// An absolute Unix path becomes file:///abs/path. A Windows drive path becomes
// the RFC 8089 form file:///C:/dir/file (slash-separated, percent-escaped).
// The parseFileURL function folds that form back to the native path.
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

// nativePath maps the path of a parsed file:// URI to the filesystem path for
// goos. On Windows the RFC 8089 drive form file:///C:/dir/file parses to
// /C:/dir/file. The leading slash separates the empty host from the drive, so
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

// DumpPath returns the filesystem path that a file:// URL refers to. Open resolves
// the same path, and the decode cache records it in its header. A caller can map a
// saved file source to its cache entry (iq cache clear @src).
func DumpPath(rawURL string) (string, error) {
	path, _, _, err := parseFileURL(rawURL)
	return path, err
}

// DetectFormat reports the dump format that a file:// URL resolves to. An explicit
// ?format= wins. Otherwise it sniffs the file content. A caller (iq ls -v) can name
// a file source's format without a Store.
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

// parseFileURL splits a file:// URI into its path, an optional forced format
// from the ?format= query, and the ?types=/?keys= schema hints. A native dump
// reader can need these hints. It rejects a non-file scheme and an empty path.
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
	if f := q.Get(paramFormat); f != "" {
		format, err = ParseFormat(f)
		if err != nil {
			return "", FormatUnknown, Hints{}, err
		}
	}
	return path, format, hintsFrom(q), nil
}

// urlPath returns the path that a parsed file:// URI names. That is its path,
// else its opaque part (the file:relative form). A host other than localhost is
// folded back in.
func urlPath(u *url.URL) string {
	path := u.Path
	if path == "" {
		path = u.Opaque // file:relative form.
	}
	if u.Host != "" && u.Host != "localhost" {
		// file://segment/... puts the first segment in Host. Fold it back, so a
		// two-slash relative-looking URI still names a path and is not lost.
		path = u.Host + path
	}
	return path
}

// hintsFrom reads the schema hints from the query of a file:// URL.
func hintsFrom(q url.Values) Hints {
	return Hints{
		Types:   q.Get(paramTypes),
		Keys:    q.Get(paramKeys),
		Columns: q.Get(paramColumns),
		Label:   q.Get(paramLabel),
		Rel:     q.Get(paramRel),
		Key:     q.Get(paramKey),
	}
}

// records is the cache-aware decode entry point of every read path. If a fresh
// decode cache exists, it streams the cache. Otherwise it decodes the dump. If
// populate is set and the dump is cacheable, it also tees the decode into a new
// cache, installed only after a complete pass. The populate flag is false for
// Get, because its early stop persists a partial dump. Only a full scan
// populates.
func (s *Store) records(ctx context.Context, populate bool, fn func(batch []query.Record) error) error {
	if src, ok := s.cacheSource(); ok {
		return src(ctx, fn)
	}
	// An unbounded read never writes the cache. Its records can hold an RDB key
	// above the element limit, and a later bounded read would get them from the
	// cache without the limit.
	if populate && !query.Unbounded(ctx) {
		if m, ok := s.cacheable(); ok {
			return s.populate(ctx, m, fn)
		}
	}
	return s.recordsDirect(ctx, fn)
}

// recordsDirect decodes the dump straight from its bytes, with no cache. The dump is
// a file (reopened per operation) or the piped stdin stream (read once).
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
// magic without consuming it. A non-gzip stream passes through untouched.
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

// reader yields a reader over the dump. It is a freshly reopened file, so scans
// and Get each re-read from the start. For piped stdin it is the stream itself,
// and only the first call gets it.
func (s *Store) reader() (io.Reader, func() error, error) {
	if s.pipe != nil {
		r, err := s.pipe.take()
		return r, func() error { return nil }, err
	}
	f, err := os.Open(s.path) //nolint:gosec // the path is a user-supplied dump file, by design.
	if err != nil {
		return nil, nil, fmt.Errorf("open dump %q: %w", s.path, err)
	}
	return f, f.Close, nil
}

// RecordSourceFor returns the RecordSource that decodes r for a given format.
// It is the one decoder-dispatch shared by the file:// store, the piped stdin
// store, and the cmd move importer. So every reader honors the same format set.
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
	// A fresh indexed cache resolves the keys by decoding only candidate pages. On
	// any miss it reports false and the streaming path below runs unchanged.
	if out, ok := s.cacheGet(ctx, keys); ok {
		return out, nil
	}
	l := newKeyLookup(keys)
	err := s.records(ctx, false, func(recs []query.Record) error {
		if slices.ContainsFunc(recs, l.take) {
			return errStopScan
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

// ExplainPlan describes the work of a file query, without opening the file: a full
// decode, then a client-side filter or scan. A dump file has no server. If a scan
// carries a compiled predicate, an uncached typed-JSONL dump also runs the rawpred
// prefilter. Every other format, and any scan served from a fresh decode cache,
// decodes in full. The plan is static, so it names this condition and does not
// resolve the format of the dump.
func ExplainPlan(keys selector.KeySet, pred predicate.Node, _ bool) query.AccessPlan {
	if !keys.Scan && len(keys.Keys) > 0 {
		return query.AccessPlan{Ops: []string{
			"decode dump file",
			fmt.Sprintf("filter to %d key(s) client-side", len(keys.Keys)),
		}}
	}
	prefilter := query.StagePlan{State: query.StageOff, Note: "no compiled predicate"}
	if pred != nil {
		prefilter = query.StagePlan{
			State: query.StageConditional,
			Scope: "rawpred on the raw value of each record, before decode",
			Note:  "only for an uncached typed-JSONL dump",
		}
	}
	return query.AccessPlan{
		Ops:       []string{"decode dump file", "scan client-side"},
		Server:    query.StagePlan{Note: "a dump file has no server"},
		Prefilter: prefilter,
	}
}

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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// pageSize bounds how many records a scan buffers before handing a page to the
// caller, so a stream holds only one page in memory.
const pageSize = 500

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
	cache  CacheConfig // decode-cache policy; zero value disables caching.
}

// Open resolves a file:// URL to a read-only dump Store. The URL path is the dump
// file; an optional ?format= (rdb|bson|mongoexport|jsonl|yaml) overrides content
// detection for the ambiguous text formats. cache is the decode-cache policy; a
// zero CacheConfig disables caching, so a caller that wires none keeps the
// always-decode behavior.
func Open(rawURL string, dec numfmt.DecimalMode, cache CacheConfig) (*Store, error) {
	path, forced, err := parseFileURL(rawURL)
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
	return &Store{path: path, format: format, dec: dec, cache: cache}, nil
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

// DumpPath returns the filesystem path a file:// URL refers to — the same path
// Open resolves and the decode cache records in its header — so a caller can map
// a saved file source to its cache entry (iq cache clear @src).
func DumpPath(rawURL string) (string, error) {
	path, _, err := parseFileURL(rawURL)
	return path, err
}

// DetectFormat reports the dump format a file:// URL resolves to: an explicit
// ?format= wins, otherwise the content is sniffed from the file. Exported so a
// caller (iq ls -v) can name a file source's format without building a Store.
func DetectFormat(rawURL string) (Format, error) {
	path, forced, err := parseFileURL(rawURL)
	if err != nil {
		return FormatUnknown, err
	}
	if forced != FormatUnknown {
		return forced, nil
	}
	return detectFormat(path)
}

// parseFileURL splits a file:// URL into its path and an optional forced format
// from the ?format= query. It rejects a non-file scheme and an empty path.
func parseFileURL(raw string) (path string, format Format, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", FormatUnknown, fmt.Errorf("parse file url: %w", err)
	}
	if u.Scheme != "file" {
		return "", FormatUnknown, fmt.Errorf("not a file url: %q", raw)
	}
	path = u.Path
	if path == "" {
		path = u.Opaque // file:relative form.
	}
	if u.Host != "" && u.Host != "localhost" {
		// file://segment/... puts the first segment in Host; fold it back so a
		// two-slash relative-looking url still names a path rather than being lost.
		path = u.Host + path
	}
	if path == "" {
		return "", FormatUnknown, errors.New("file url has no path")
	}
	if f := u.Query().Get("format"); f != "" {
		format, err = ParseFormat(f)
		if err != nil {
			return "", FormatUnknown, err
		}
	}
	return path, format, nil
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
	src, err := RecordSourceFor(r, s.format, s.dec)
	if err != nil {
		return err
	}
	return src(ctx, fn)
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
func RecordSourceFor(r io.Reader, format Format, dec numfmt.DecimalMode) (query.RecordSource, error) {
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
		batch := make(map[string]any, len(recs))
		for _, r := range recs {
			batch[r.Key] = r.Value
		}
		return fn(batch)
	})
}

// Get resolves specific keys with a single filtered streaming pass, stopping once
// every requested key is found. A key not in the dump maps to nil, matching the
// KVStore contract.
func (s *Store) Get(ctx context.Context, keys []string) (map[string]any, error) {
	if len(keys) == 0 {
		return map[string]any{}, nil
	}
	// A fresh indexed cache resolves the keys by decoding only candidate pages; on
	// any miss it reports false and the streaming path below runs unchanged.
	if out, ok := s.cacheGet(ctx, keys); ok {
		return out, nil
	}
	want := make(map[string]struct{}, len(keys))
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		want[k] = struct{}{}
		out[k] = nil
	}
	found := make(map[string]bool, len(keys))
	err := s.records(ctx, false, func(recs []query.Record) error {
		for _, r := range recs {
			if _, ok := want[r.Key]; !ok {
				continue
			}
			out[r.Key] = r.Value
			if !found[r.Key] {
				found[r.Key] = true
				if len(found) == len(want) {
					return errStopScan
				}
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopScan) {
		return nil, err
	}
	return out, nil
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
func ExplainPlan(keys selector.KeySet, _ predicate.Node, _ bool) query.AccessPlan {
	if !keys.Scan && len(keys.Keys) > 0 {
		return query.AccessPlan{Ops: []string{
			"decode dump file",
			fmt.Sprintf("filter to %d key(s) client-side", len(keys.Keys)),
		}}
	}
	return query.AccessPlan{Ops: []string{"decode dump file", "scan client-side"}}
}

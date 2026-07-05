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
	format Format
	dec    numfmt.DecimalMode
}

// Open resolves a file:// URL to a read-only dump Store. The URL path is the dump
// file; an optional ?format= (rdb|bson|mongoexport|jsonl) overrides content
// detection for the ambiguous JSON-text formats.
func Open(rawURL string, dec numfmt.DecimalMode) (*Store, error) {
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
	return &Store{path: path, format: format, dec: dec}, nil
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
		format, err = parseFormat(f)
		if err != nil {
			return "", FormatUnknown, err
		}
	}
	return path, format, nil
}

// records is the single decode entry point: it opens the dump, builds the source
// for its format, and drives it. Every read path (TypedScan, ScanBatches, Get)
// flows through here, so the file is reopened and streamed per operation.
func (s *Store) records(ctx context.Context, fn func(batch []query.Record) error) error {
	f, err := os.Open(s.path) //nolint:gosec // the path is a user-supplied dump file, by design.
	if err != nil {
		return fmt.Errorf("open dump %q: %w", s.path, err)
	}
	defer func() { _ = f.Close() }()
	src, err := s.source(f)
	if err != nil {
		return err
	}
	return src(ctx, fn)
}

// source returns the RecordSource that decodes r for the store's format.
func (s *Store) source(r io.Reader) (query.RecordSource, error) {
	switch s.format {
	case FormatJSONL:
		return query.JSONLSource(r, pageSize, false), nil
	case FormatRDB:
		return rdbSource(r, pageSize), nil
	case FormatBSON:
		return bsonSource(r, pageSize, s.dec), nil
	case FormatMongoexport:
		return extJSONSource(r, pageSize, s.dec), nil
	default:
		return nil, fmt.Errorf("unknown dump format for %q", s.path)
	}
}

// TypedScan streams the dump as typed records, so a copy from a file reconstructs
// each item's native structure.
func (s *Store) TypedScan(ctx context.Context, fn func(batch []query.Record) error) error {
	return s.records(ctx, fn)
}

// ScanBatches streams the dump as {key: value} pages, dropping the type tag for
// the jq read path.
func (s *Store) ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error {
	return s.records(ctx, func(recs []query.Record) error {
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
	want := make(map[string]struct{}, len(keys))
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		want[k] = struct{}{}
		out[k] = nil
	}
	found := make(map[string]bool, len(keys))
	err := s.records(ctx, func(recs []query.Record) error {
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

package file

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/buger/jsonparser"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/rawpred"
)

// The file store answers the optional filtered-scan port with a client-side
// raw-byte prefilter, so the engine prefers it over a plain ScanBatches when a
// filter compiles.
var _ query.FilteredScanner = (*Store)(nil)

// ScanFiltered answers the query.FilteredScanner port: it streams the dump like
// ScanBatches but, before decoding, drops any record a pushed predicate provably
// cannot match. The predicate is a conservative superset and the engine re-runs the
// full jq over every returned page, so the prefilter only shrinks how much this
// driver decodes — it never changes results.
//
// The prefilter runs only for an uncached typed-JSONL dump (iq's own {key,type,value}
// JSON Lines / array, the sole format whose on-disk bytes are the JSON the engine
// filters). Every other case falls back to the exact ScanBatches behavior: a non-JSONL
// format (YAML/CSV/BSON/RDB/mongoexport/DynamoDB-AV/Neo4j-APOC), whose byte shape does
// not match jq's view of the value, and a dump served from a fresh CBOR decode cache,
// whose bytes are already-decoded CBOR a JSON prefilter can never read. Both of those
// are already fast (the cache streams decoded records; a small dump decodes sub-perceptibly),
// so the fallback costs nothing the prefilter would save.
//
// A prefiltered scan deliberately does NOT populate the decode cache: cache population
// requires decoding every record, which is exactly the work the prefilter skips, so
// teeing a cache here would defeat the optimization. A later full scan (ScanBatches /
// TypedScan) still populates the cache as before; only this filtered path opts out.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	if !s.prefilterable() {
		// Non-JSONL format, or a fresh cache: the plain scan is already fast and its
		// bytes are not filterable JSON, so re-use ScanBatches unchanged. checked stays
		// zero, which the class tests assert as the bypass signature.
		return s.ScanBatches(ctx, fn)
	}
	// Prepare the predicate once for the whole scan: NewMatcher compiles every Regex
	// pattern here so the per-record path never recompiles one.
	matcher := rawpred.NewMatcher(pred)
	return s.prefilterJSONL(ctx, matcher, fn)
}

// prefilterable reports whether ScanFiltered's raw-byte prefilter applies to this
// store: the format must be typed JSONL (the only format whose stored bytes are the
// JSON the engine filters over), and no fresh decode cache may exist (a cache stores
// already-decoded CBOR, unfilterable, and streaming it is already fast). It reuses
// the exact cacheSource freshness check, so the route decision and the cache read
// agree on what "fresh" means.
func (s *Store) prefilterable() bool {
	if s.format != FormatJSONL {
		return false
	}
	_, fresh := s.cacheSource()
	return !fresh
}

// prefilterJSONL streams the dump's typed-JSONL bytes, dropping every record the
// matcher proves cannot match before it is decoded, and hands the survivors to fn in
// {key: value} pages. It mirrors query.JSONSource's array-tolerant line/RawMessage
// loop exactly — the core has no pre-decode seam, so the driver duplicates the loop —
// inserting one raw-byte check between reading a record's raw bytes and decoding it.
// It never touches the decode cache, so a prefiltered scan leaves the cache unpopulated.
func (s *Store) prefilterJSONL(ctx context.Context, matcher *rawpred.Matcher, fn func(batch map[string]any) error) error {
	r, closeR, err := s.reader()
	if err != nil {
		return err
	}
	defer func() { _ = closeR() }()
	dr, err := maybeGunzip(r)
	if err != nil {
		return err
	}
	return scanFilteredJSON(ctx, dr, matcher, &s.prefilterChecked, &s.prefilterSkipped, fn)
}

// scanFilteredJSON is the driver-owned duplicate of query.JSONSource's scan loop with
// a raw-byte prefilter spliced in. It reads either concatenated JSON objects (JSON
// Lines) or a single top-level array, decodes each survivor with the same UseNumber +
// exact-integer semantics as the core decodeLine, and flushes {key: value} pages of
// pageSize records — the same page shape ScanBatches produces. checked and skipped
// count every record the prefilter evaluated and the subset it dropped.
//
// For each record the matcher runs over the envelope's raw `value` bytes, never the
// whole {key,type,value} line: the engine filters over the value, so matching the whole
// envelope would evaluate the predicate's paths against the wrong object. A record whose
// value the matcher proves the predicate rejects is skipped without decoding; every other
// record is decoded exactly as the plain scan would and its key/value added to the page.
func scanFilteredJSON(ctx context.Context, r io.Reader, matcher *rawpred.Matcher, checked, skipped *int, fn func(batch map[string]any) error) error {
	br := bufio.NewReader(r)
	array, err := startsJSONArray(br)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("read json: %w", err)
	}
	dec := json.NewDecoder(br)
	if array {
		if _, err := dec.Token(); err != nil { // consume '['
			return fmt.Errorf("read json array: %w", err)
		}
	}
	page := make([]query.Record, 0, pageSize)
	emit := func() error {
		if len(page) == 0 {
			return nil
		}
		batch := make(map[string]any, len(page))
		for _, rec := range page {
			batch[rec.Key] = rec.Value
		}
		page = page[:0]
		return fn(batch)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if array && !dec.More() {
			break
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if !array && errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("decode json record: %w", err)
		}
		// The engine filters over the value, so the prefilter runs on the extracted raw
		// `value` bytes. A value that cannot be extracted (a malformed or value-less line)
		// is not evaluated — it is left for query.DecodeTypedRecord, which reports the same error
		// the plain scan would. So is an envelope that repeats `value`.
		if val, ok := typedValue(raw); ok {
			*checked++
			if matcher.Match(val) == rawpred.CannotMatch {
				*skipped++
				continue
			}
		}
		rec, err := query.DecodeTypedRecord(raw)
		if err != nil {
			return err
		}
		page = append(page, rec)
		if len(page) >= pageSize {
			if err := emit(); err != nil {
				return err
			}
		}
	}
	return emit()
}

// typedValue returns the raw `value` of a typed record envelope, or ok=false when the
// envelope is malformed, has no `value`, or repeats it. encoding/json matches field
// names without regard to case and keeps the last match, so `value`, `Value` and
// `VALUE` all count, and a repeated one cannot be judged from raw bytes: it goes to
// the full decode.
func typedValue(raw []byte) ([]byte, bool) {
	var val []byte
	count := 0
	err := jsonparser.ObjectEach(raw, func(k, v []byte, _ jsonparser.ValueType, _ int) error {
		if strings.EqualFold(string(k), "value") {
			count++
			val = v
		}
		return nil
	})
	return val, err == nil && count == 1
}

// startsJSONArray reports whether the first non-whitespace byte is '[' (a top-level
// array), without consuming any value bytes. It duplicates query.startsJSONArray so the
// driver's prefilter loop reads a dump exactly as the core JSONSource does.
func startsJSONArray(br *bufio.Reader) (bool, error) {
	for {
		b, err := br.Peek(1)
		if err != nil {
			return false, err
		}
		switch b[0] {
		case ' ', '\t', '\r', '\n':
			if _, err := br.Discard(1); err != nil {
				return false, err
			}
		case '[':
			return true, nil
		default:
			return false, nil
		}
	}
}

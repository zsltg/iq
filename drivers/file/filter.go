package file

import (
	"context"
	"io"
	"strings"

	"github.com/buger/jsonparser"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/rawpred"
)

// The file store answers the optional filtered-scan port with a client-side
// raw-byte prefilter. So the engine prefers it over a plain ScanBatches when a
// filter compiles.
var _ query.FilteredScanner = (*Store)(nil)

// ScanFiltered drops records that the pushed predicate provably cannot match,
// before it decodes them. See query.FilteredScanner and rawpred.
//
// The prefilter runs only for an uncached typed-JSONL dump. Typed JSONL is the
// only format whose on-disk bytes are the JSON that the engine filters. Every
// other format falls back to ScanBatches, because its byte shape does not match
// the jq view of the value. A dump served from a fresh CBOR decode cache also
// falls back, because a JSON prefilter cannot read decoded CBOR bytes.
//
// A prefiltered scan does not populate the decode cache. The cache needs every
// record decoded, which is the work that the prefilter skips.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	if !s.prefilterable() {
		// The class tests assert that checked stays zero on this path.
		return s.ScanBatches(ctx, fn)
	}
	matcher := rawpred.NewMatcher(pred, numfmt.DecimalAuto)
	return s.prefilterJSONL(ctx, matcher, fn)
}

// prefilterable reports whether the raw-byte prefilter applies: the format is
// typed JSONL and no fresh decode cache exists. It uses the same cacheSource
// check as the cache read, so both agree on what "fresh" means.
func (s *Store) prefilterable() bool {
	if s.format != FormatJSONL {
		return false
	}
	_, fresh := s.cacheSource()
	return !fresh
}

// prefilterJSONL streams the typed-JSONL dump with query.JSONStream. It checks
// the raw bytes of each record against the matcher before it decodes the record.
// It never touches the decode cache.
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
	f := recordFilter{matcher: matcher, checked: &s.prefilterChecked, skipped: &s.prefilterSkipped}
	return scanFilteredJSON(ctx, dr, f, fn)
}

// recordFilter drops a typed record whose raw value the matcher proves cannot
// match, and counts what it evaluated and what it dropped.
type recordFilter struct {
	matcher          *rawpred.Matcher
	checked, skipped *int
}

// drop reports whether the record in raw can be skipped without a decode. The
// matcher sees only the raw `value` bytes, because the engine filters over the
// value and not over the whole {key,type,value} line. If the value cannot be
// extracted (a malformed line, no value, or a repeated one), drop keeps the
// record. Then query.DecodeTypedRecord reports the same error as the plain scan.
func (f recordFilter) drop(raw []byte) bool {
	val, ok := typedValue(raw)
	if !ok {
		return false
	}
	*f.checked++
	if f.matcher.Match(val) != rawpred.CannotMatch {
		return false
	}
	*f.skipped++
	return true
}

// scanFilteredJSON runs the raw-byte prefilter inside the loop of
// query.JSONSource. It reads JSON Lines or one top-level array, decodes each
// survivor, and flushes {key: value} pages of pageSize records. The filter counts
// the records it evaluated and the records it dropped.
func scanFilteredJSON(ctx context.Context, r io.Reader, f recordFilter, fn func(batch map[string]any) error) error {
	stream, err := query.NewJSONStream(r)
	if err != nil {
		return err
	}
	if stream == nil {
		return nil // empty input: no records, and no context check.
	}
	pg := newRecordPager(pageSize, func(recs []query.Record) error { return fn(recordBatch(recs)) })
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, ok, err := stream.Next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if f.drop(raw) {
			continue
		}
		rec, err := query.DecodeTypedRecord(raw)
		if err != nil {
			return err
		}
		if err := pg.add(rec); err != nil {
			return err
		}
	}
	return pg.flush()
}

// recordBatch folds a page of records into a {key: value} map. A key that repeats in
// one page keeps its last value.
func recordBatch(recs []query.Record) map[string]any {
	batch := make(map[string]any, len(recs))
	for _, r := range recs {
		batch[r.Key] = r.Value
	}
	return batch
}

// typedValue returns the raw `value` of a typed record envelope. It returns
// ok=false if the envelope is malformed, has no `value`, or repeats it.
// The encoding/json package matches field names without case and keeps the
// last match, so `value`, `Value` and `VALUE` all count. Raw bytes cannot
// judge a repeated field, so the record goes to the full decode.
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

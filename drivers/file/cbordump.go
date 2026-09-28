package file

import (
	"fmt"
	"math"
	"reflect"

	"github.com/fxamacker/cbor/v2"

	"github.com/zsltg/iq/internal/query"
)

// The decode cache serializes the normalized record stream with CBOR. The codec
// options here are load-bearing: fxamacker's defaults decode a CBOR map to
// map[interface{}]interface{}, a CBOR integer to uint64/int64, and reject a
// text string that is not valid UTF-8 — none of which match the value model the
// query engine (gojq) accepts, and the last of which is fatal for Redis values
// and hash-field keys, which are arbitrary bytes decoded to a Go string. The
// pinned options plus narrowInts reproduce Normalize's closed set exactly:
// nil, bool, int, float64, string, map[string]any, []any. See cbordump_test.go,
// which proves the round-trip is type-identical for every member of that set.

// cborEnc marshals with default options: it does not validate UTF-8 (so a
// binary Redis string encodes verbatim) and does not shrink floats (so a
// float64 stays bit-exact).
var cborEnc = mustEncMode(cbor.EncOptions{})

// cborDec is the counterpart decoder. DefaultMapType returns objects as
// map[string]any; UTF8DecodeInvalid accepts the non-UTF-8 strings cborEnc
// wrote, returning their bytes unchanged rather than erroring. Integers still
// arrive as uint64/int64 — narrowInts converts them to int after decode.
// TagsForbidden rejects any CBOR tag: the closed value set never encodes one,
// so a tag means a corrupted cache. Without it, tag 1 decoded to a time.Time,
// a value outside the set that re-encodes as something else (FuzzCBORRecord).
var cborDec = mustDecMode(cbor.DecOptions{
	DefaultMapType: reflect.TypeFor[map[string]any](),
	UTF8:           cbor.UTF8DecodeInvalid,
	TagsMd:         cbor.TagsForbidden,
})

// mustEncMode builds an immutable CBOR encode mode from static options. The
// options are compile-time constants proven valid, so a build error is a
// programmer error, not a runtime condition.
func mustEncMode(opts cbor.EncOptions) cbor.EncMode {
	em, err := opts.EncMode()
	if err != nil {
		panic(fmt.Sprintf("file: build cbor enc mode: %v", err))
	}
	return em
}

// mustDecMode builds an immutable CBOR decode mode from static options.
func mustDecMode(opts cbor.DecOptions) cbor.DecMode {
	dm, err := opts.DecMode()
	if err != nil {
		panic(fmt.Sprintf("file: build cbor dec mode: %v", err))
	}
	return dm
}

// wireRecord is the on-disk form of a query.Record: a fixed 3-element CBOR array
// [key, type, value], compact and independent of the struct's field names.
type wireRecord struct {
	_     struct{} `cbor:",toarray"`
	Key   string
	Type  string
	Value any
}

// encodeRecord marshals one record to its CBOR array form.
func encodeRecord(r query.Record) ([]byte, error) {
	b, err := cborEnc.Marshal(wireRecord{Key: r.Key, Type: r.Type, Value: r.Value})
	if err != nil {
		return nil, fmt.Errorf("encode cache record %q: %w", r.Key, err)
	}
	return b, nil
}

// decodeRecord unmarshals one CBOR array form back into a query.Record, narrowing
// the value's integers to int so it matches the shape the live decode produced.
func decodeRecord(dec *cbor.Decoder) (query.Record, error) {
	var w wireRecord
	if err := dec.Decode(&w); err != nil {
		return query.Record{}, err
	}
	return query.Record{Key: w.Key, Type: w.Type, Value: narrowInts(w.Value)}, nil
}

// narrowInts converts every integer a CBOR decode produced (uint64 or int64) to
// Go int, recursing through maps and slices, so a cached value's type model
// matches Normalize's, which narrows all integers to int. A uint64 beyond int's
// range (unreachable from normalized data, which is int-bounded before caching)
// degrades to float64 rather than overflowing.
func narrowInts(v any) any {
	switch t := v.(type) {
	case uint64:
		if t <= math.MaxInt {
			return int(t)
		}
		return float64(t)
	case int64:
		return int(t)
	case map[string]any:
		for k, e := range t {
			t[k] = narrowInts(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = narrowInts(e)
		}
		return t
	default:
		return v
	}
}

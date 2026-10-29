package file

import (
	"fmt"
	"math"
	"reflect"

	"github.com/fxamacker/cbor/v2"

	"github.com/zsltg/iq/internal/query"
)

// The decode cache serializes the normalized record stream with CBOR. The codec
// options matter. The defaults of fxamacker decode a CBOR map to
// map[interface{}]interface{} and a CBOR integer to uint64/int64. They also
// reject a text string that is not valid UTF-8. None of that matches the value
// model that gojq accepts. The last default is fatal for Redis values and
// hash-field keys, which are arbitrary bytes decoded to a Go string. The pinned
// options plus narrowInts reproduce the closed set of Normalize: nil, bool, int,
// float64, string, map[string]any, []any. The test file cbordump_test.go proves
// that the round-trip is type-identical for every member of that set.

// cborEnc marshals with default options, plus one rule for big integers. It does
// not validate UTF-8, so a binary Redis string encodes verbatim. It does not
// shrink floats, so a float64 stays bit-exact. BigIntConvertReject makes every
// *big.Int fail to encode. The cache cannot hold one. The decoder forbids all
// tags, so a bignum tag would not read back. A *big.Int that fits in uint64
// would encode as a plain integer and read back as a float64 or an int64. The
// cache writer treats the encode error as a signal to skip the cache for that
// dump. The cold decode keeps working.
var cborEnc = mustEncMode(cbor.EncOptions{BigIntConvert: cbor.BigIntConvertReject})

// cborDec is the counterpart decoder. DefaultMapType returns objects as
// map[string]any. UTF8DecodeInvalid accepts the non-UTF-8 strings that cborEnc
// wrote and returns their bytes unchanged. IntDecConvertSignedOrFail decodes every
// integer as an int64 and rejects an integer outside the int64 range. cborEnc never
// writes such an integer, so one means a corrupted cache. Without this rule, an
// integer below the int64 range decodes to a big.Int, which cborEnc then refuses
// (FuzzCBORRecord). narrowInts converts the int64 values to int after decode.
// TagsForbidden rejects any CBOR tag.
// The closed value set never encodes a tag, so a tag means a corrupted cache.
// Without it, tag 1 decodes to a time.Time, which is outside the set and re-encodes
// as something else (FuzzCBORRecord).
var cborDec = mustDecMode(cbor.DecOptions{
	DefaultMapType: reflect.TypeFor[map[string]any](),
	UTF8:           cbor.UTF8DecodeInvalid,
	IntDec:         cbor.IntDecConvertSignedOrFail,
	TagsMd:         cbor.TagsForbidden,

	MaxNestedLevels:  maxCBORNestedLevels,
	MaxArrayElements: math.MaxInt32,
	MaxMapPairs:      math.MaxInt32,
})

// maxCBORNestedLevels is the largest nesting depth that fxamacker/cbor allows.
// It is above the 10000 levels that encoding/json accepts from a dump.
const maxCBORNestedLevels = 65535

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

// narrowInts converts every integer that a CBOR decode produced (int64) to Go
// int, recursing through maps and slices. So a cached value's type model matches
// Normalize's, which narrows all integers to int.
func narrowInts(v any) any {
	switch t := v.(type) {
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

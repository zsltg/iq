package file

import (
	"bytes"
	"context"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// maxFuzzInput bounds one fuzz input, maxFuzzRecords bounds how much of a dump one
// input decodes, and fuzzDrainBudget bounds how long a drain runs. A dump reader
// streams, and the binary readers size a buffer from a length field the input
// controls, so an unbounded drain would spend the -fuzztime budget on one input.
// maxDumpFuzzInput is tighter than maxFuzzInput for the same reason: a larger blob
// of hostile bytes reaches no new reader state.
const (
	maxFuzzInput     = 64 << 10
	maxDumpFuzzInput = 4 << 10
	maxFuzzRecords   = 200
	fuzzDrainBudget  = 2 * time.Second
)

// errFuzzEnough stops a drain at maxFuzzRecords. It never leaves the target.
var errFuzzEnough = errors.New("fuzz record cap reached")

// fuzzFormats lists every Format a fuzz input can select, FormatUnknown first so
// content detection is exercised too.
var fuzzFormats = []Format{
	FormatUnknown,
	FormatJSONL,
	FormatYAML,
	FormatMongoexport,
	FormatBSON,
	FormatRDB,
	FormatDynamoDBJSON,
	FormatCassandraCSV,
	FormatNeo4jJSON,
}

// FuzzOpenReader drives the dump readers with arbitrary bytes under every format,
// which is the untrusted path a piped stdin takes. The oracle is that no input
// panics: OpenReader either detects a format or reports an error, and the record
// source either streams records or reports an error. The format name of every
// Format also round-trips through ParseFormat, so a detected format is always a
// value a user can force.
func FuzzOpenReader(f *testing.F) {
	// Seeds cover one well-formed dump per sniffable format plus hostile bytes:
	// empty, invalid UTF-8, a truncated BSON length header and deep nesting.
	seeds := []struct {
		data []byte
		sel  byte
	}{
		{[]byte(`{"key":"k","type":"string","value":"v"}`), 0},
		{[]byte(`[{"key":"k","type":"string","value":1}]`), 1},
		{[]byte("key: k\ntype: string\nvalue: v\n"), 2},
		{[]byte(`{"_id":{"$oid":"5f1d7f8e"},"n":1}`), 3},
		{[]byte("\x05\x00\x00\x00\x00"), 4},
		{[]byte("REDIS0011"), 5},
		{[]byte(`{"Item":{"pk":{"S":"a"}}}`), 6},
		{[]byte("id,name\n1,a\n"), 7},
		{[]byte(`{"type":"node","id":"1","labels":["L"],"properties":{"a":1}}`), 8},
		{[]byte(""), 0},
		{[]byte("\xff\xfe\xfd"), 0},
		{[]byte("\xff\x00\x00\x00"), 4},
		{[]byte(`{"key":"k","type":"json","value":[[[[[[[[[[1]]]]]]]]]]}`), 1},
		{[]byte(`{"key":"","type":"string","value":null}`), 1},
	}
	for _, s := range seeds {
		f.Add(s.data, s.sel)
	}

	f.Fuzz(func(t *testing.T, data []byte, sel byte) {
		if len(data) > maxDumpFuzzInput {
			t.Skip("input over the fuzz size bound")
		}
		// Every format names itself with a value ParseFormat accepts, so a detected
		// format can always be forced back with ?format=.
		for _, want := range fuzzFormats[1:] {
			got, err := ParseFormat(want.String())
			require.NoErrorf(t, err, "Format %d does not name itself", want)
			require.Equalf(t, want, got, "Format %d does not round-trip through ParseFormat", want)
		}

		format := fuzzFormats[int(sel)%len(fuzzFormats)]
		store, err := OpenReader(data, format, numfmt.DecimalAuto)
		if err != nil {
			return // detection refused the bytes, which is a clean rejection.
		}
		require.NotNil(t, store, "OpenReader returned neither a store nor an error")

		src, err := RecordSourceFor(bytes.NewReader(data), store.format, store.dec, Hints{})
		if err != nil {
			return // the format needs hints this target does not supply.
		}
		ctx, cancel := context.WithTimeout(context.Background(), fuzzDrainBudget)
		defer cancel()
		count := 0
		err = src(ctx, func(batch []query.Record) error {
			count += len(batch)
			if count >= maxFuzzRecords {
				return errFuzzEnough
			}
			return nil
		})
		if err != nil && !errors.Is(err, errFuzzEnough) {
			return // a malformed dump reports an error, which is the contract.
		}
	})
}

// FuzzCBORRecord drives the decode cache's record codec with arbitrary bytes, which
// is what a corrupted or truncated cache file hands it. The oracle is that a
// decoded record survives a re-encode: decodeRecord never panics, and encoding what
// it produced and decoding that again gives an identical record, so narrowInts is
// idempotent over everything the decoder can build.
func FuzzCBORRecord(f *testing.F) {
	// Seeds are real encodeRecord output over the closed value set, plus hostile
	// bytes: empty, a truncated array header and an invalid UTF-8 text string.
	values := []any{
		nil,
		true,
		42,
		math.MaxInt,
		1.5,
		"v",
		"\xff\xfe",
		map[string]any{"a": 1, "b": []any{1, "x", nil}},
		[]any{},
	}
	for i, v := range values {
		b, err := encodeRecord(query.Record{Key: "k", Type: "json", Value: v})
		if err != nil {
			f.Fatalf("seed %d does not encode: %v", i, err)
		}
		f.Add(b)
	}
	f.Add([]byte{})
	f.Add([]byte{0x83})
	f.Add([]byte{0x83, 0x61, 0xff, 0x60, 0xf6})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFuzzInput {
			t.Skip("input over the fuzz size bound")
		}
		rec, err := decodeRecord(cborDec.NewDecoder(bytes.NewReader(data)))
		if err != nil {
			return // a corrupted cache reports an error, which is the contract.
		}
		if hasNaN(rec.Value) {
			t.Skip("a NaN never compares equal to itself")
		}
		b, err := encodeRecord(rec)
		require.NoErrorf(t, err, "a decoded record must re-encode: %#v", rec)

		again, err := decodeRecord(cborDec.NewDecoder(bytes.NewReader(b)))
		require.NoErrorf(t, err, "re-encoded record must decode: %#v", rec)
		require.Equalf(t, rec, again, "the cache codec is not a round trip\n record: %#v", rec)
	})
}

// hasNaN reports whether v holds a NaN float, recursing through maps and slices. A
// NaN is not equal to itself, so a record carrying one cannot be compared by value
// and the round-trip oracle passes over it.
func hasNaN(v any) bool {
	switch t := v.(type) {
	case float64:
		return math.IsNaN(t)
	case float32:
		return math.IsNaN(float64(t))
	case map[string]any:
		for _, e := range t {
			if hasNaN(e) {
				return true
			}
		}
		return false
	case []any:
		return slices.ContainsFunc(t, hasNaN)
	default:
		return false
	}
}

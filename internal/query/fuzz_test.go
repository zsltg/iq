package query_test

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// maxFuzzInput bounds one fuzz input, and maxFuzzRecords bounds how many records
// one input round-trips. A dump source streams, so an unbounded drain would spend
// the -fuzztime budget on one input.
const (
	maxFuzzInput   = 64 << 10
	maxFuzzRecords = 200
)

// errFuzzEnough stops a drain at maxFuzzRecords. It never leaves the target.
var errFuzzEnough = errors.New("fuzz record cap reached")

// FuzzJSONLRoundTrip drives the typed dump codec with arbitrary JSON Lines. The
// values come in through the plain reader, which is the foreign-input path, and go
// back out through WriteJSONL, which documents itself as that reader's inverse.
//
// Two oracles hold. A typed dump is a fixpoint: writing what the typed reader read
// gives byte-identical output, so a dump reloads and re-dumps without drifting.
// JSONSource is the array-tolerant counterpart of JSONLSource, so both must read
// the same typed dump into the same records.
func FuzzJSONLRoundTrip(f *testing.F) {
	// Seeds from the dump_test.go tables, plus hostile input: empty, deep nesting,
	// invalid UTF-8, a number beyond float64 and a number beyond int64.
	seeds := []string{
		`{"a":1}`,
		"{\"a\":1}\n{\"b\":2}\n",
		`[1,2,3]`,
		`"plain string"`,
		`null`,
		`{"a":1,"b":[{"c":null},true,1.5]}`,
		"\n\n{\"a\":1}\n\n",
		``,
		`{"a":[[[[[[[[[[1]]]]]]]]]]}`,
		"{\"a\":\"\xff\xfe\"}",
		`{"n":100000000000000000001}`,
		`{"n":1e400}`,
		`{"n":1.0}`,
		`{"n":-0}`,
		`{"a":`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFuzzInput {
			t.Skip("input over the fuzz size bound")
		}
		recs, err := drainFuzz(query.JSONLSource(bytes.NewReader(data), 100, true))
		if err != nil {
			return // not a JSON Lines stream, which the reader reports as an error.
		}
		if len(recs) == 0 {
			return // nothing to dump.
		}
		// The plain reader keys nothing, and the typed dump refuses a record with no
		// key, so give each record a key of its own before writing it.
		for i := range recs {
			recs[i].Key = "k" + strconv.Itoa(i)
		}

		var first bytes.Buffer
		require.NoError(t, query.WriteJSONL(&first, recs), "records read from JSON must re-encode")

		viaLines, err := drainFuzz(query.JSONLSource(bytes.NewReader(first.Bytes()), 100, false))
		require.NoError(t, err, "the typed reader must read back what WriteJSONL wrote")
		viaJSON, err := drainFuzz(query.JSONSource(bytes.NewReader(first.Bytes()), 100, false))
		require.NoError(t, err, "the array-tolerant reader must read back what WriteJSONL wrote")
		require.Equal(t, viaLines, viaJSON, "JSONSource and JSONLSource disagree on one typed dump")

		var second bytes.Buffer
		require.NoError(t, query.WriteJSONL(&second, viaLines), "a re-read record must re-encode")
		require.Equal(t, first.String(), second.String(), "the typed dump is not a fixpoint")
	})
}

// drainFuzz collects a RecordSource, stopping at maxFuzzRecords. The record cap is
// reported as success, because a bounded prefix is all the round-trip oracle needs.
func drainFuzz(src query.RecordSource) ([]query.Record, error) {
	var out []query.Record
	err := src(context.Background(), func(batch []query.Record) error {
		out = append(out, batch...)
		if len(out) >= maxFuzzRecords {
			return errFuzzEnough
		}
		return nil
	})
	if err != nil && !errors.Is(err, errFuzzEnough) {
		return nil, err
	}
	return out, nil
}

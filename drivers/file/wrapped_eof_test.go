package file

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// errWrappedEOF is a reader failure that wraps io.EOF. A stream decoder returns it
// unchanged, so a loop that tests errors.Is would take it for a clean end.
var errWrappedEOF = fmt.Errorf("read chunk: %w", io.EOF)

// wrappedEOFAt serves data once. After that, every read at or past the end of
// data fails with errWrappedEOF.
type wrappedEOFAt struct{ data []byte }

func (r wrappedEOFAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r.data)) {
		return 0, errWrappedEOF
	}
	n := copy(p, r.data[off:])
	if off+int64(n) >= int64(len(r.data)) {
		return n, errWrappedEOF
	}
	return n, nil
}

// TestSourcesReportAWrappedEOF feeds each stream loop valid records and then a
// reader failure that wraps io.EOF. The loop must return that error with the
// wrap chain intact. A quiet end would lose the records after the failure. A
// bare io.EOF still ends the stream without an error.
func TestSourcesReportAWrappedEOF(t *testing.T) {
	cbor1, err := encodeRecord(query.Record{Key: "a", Type: "string", Value: "x"})
	require.NoError(t, err)
	bson1 := mustBSON(t, bson.M{"_id": "a"})

	run := func(src query.RecordSource, err error) error {
		require.NoError(t, err)
		return src(context.Background(), func([]query.Record) error { return nil })
	}
	tests := []struct {
		name string
		run  func(r io.Reader) error
		data []byte
	}{
		{"bson", func(r io.Reader) error {
			return run(bsonSource(r, pageSize, numfmt.DecimalAuto), nil)
		}, bson1},
		{"mongoexport values", func(r io.Reader) error {
			return run(extJSONSource(r, pageSize, numfmt.DecimalAuto), nil)
		}, []byte(`{"_id":1}` + "\n")},
		{"neo4j values", func(r io.Reader) error {
			return run(neo4jSource(r, pageSize, numfmt.DecimalAuto, Hints{Label: "Person"}))
		}, []byte(strings.SplitAfter(apocNodes, "\n")[0])},
		{"dynamodb values", func(r io.Reader) error {
			return run(dynamoSource(r, pageSize, numfmt.DecimalAuto, Hints{Keys: "pk"}))
		}, []byte(`{"Item":{"pk":{"S":"a"}}}` + "\n")},
		{"cassandra header", func(r io.Reader) error {
			return run(cassandraCSVSource(r, pageSize, numfmt.DecimalAuto, Hints{Keys: "id"}))
		}, nil},
		{"cassandra rows", func(r io.Reader) error {
			return run(cassandraCSVSource(r, pageSize, numfmt.DecimalAuto, Hints{Keys: "id"}))
		}, []byte("id\na\n")},
		{"cache stream", func(r io.Reader) error {
			return streamRecords(context.Background(), cborDec.NewDecoder(r), "c.cbor", func([]query.Record) error { return nil })
		}, cbor1},
		{"cache page", func(io.Reader) error { return nil }, cbor1},
	}
	for _, tt := range tests {
		t.Run(tt.name+" reports a wrapped EOF", func(t *testing.T) {
			var err error
			if tt.name == "cache page" {
				sec := wrappedEOFAt{data: tt.data}
				err = decodePage(sec, 0, int64(len(tt.data))+1024, newKeyLookup([]string{"a"}))
			} else {
				err = tt.run(&dataThenErrReader{data: string(tt.data), err: errWrappedEOF})
			}
			require.Error(t, err)
			require.ErrorIs(t, err, io.EOF, "the wrap chain must survive")
		})
	}
	for _, tt := range tests {
		if tt.name == "cache page" {
			continue
		}
		t.Run(tt.name+" ends at a bare EOF", func(t *testing.T) {
			require.NoError(t, tt.run(bytes.NewReader(tt.data)))
		})
	}
}

package file

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// gzipBytes compresses b, so a gzipped-export fixture exercises the transparent
// gunzip path.
func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, err := gw.Write(b)
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	return buf.Bytes()
}

func TestDynamoScanOutputParity(t *testing.T) {
	// `aws dynamodb scan` output: a single {"Items":[…]} object, pk-only table.
	scan := `{"Count":2,"ScannedCount":2,"Items":[
		{"pk":{"S":"a"},"n":{"N":"5"},"tags":{"SS":["y","x"]},"ok":{"BOOL":true}},
		{"pk":{"S":"b"},"nested":{"M":{"inner":{"L":[{"N":"1"},{"S":"z"}]}}},"gone":{"NULL":true}}
	]}`
	u := writeDump(t, "scan.json", []byte(scan), "format=dynamodb-json&keys=pk")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, FormatDynamoDBJSON, st.format)

	recs := collect(t, st)
	require.Equal(t, query.Record{Key: "a", Type: "item", Value: map[string]any{
		"pk": "a", "n": 5, "tags": []any{"y", "x"}, "ok": true,
	}}, recs["a"])
	require.Equal(t, query.Record{Key: "b", Type: "item", Value: map[string]any{
		"pk": "b", "nested": map[string]any{"inner": []any{1, "z"}}, "gone": nil,
	}}, recs["b"])
}

func TestDynamoExportNDJSONGzipCompositeKey(t *testing.T) {
	// Native S3 export: NDJSON, one {"Item":{…}} per line; gzipped; composite key.
	export := `{"Item":{"pk":{"S":"a"},"sk":{"N":"3"},"v":{"S":"z"}}}
{"Item":{"pk":{"S":"a"},"sk":{"N":"10"},"v":{"S":"w"}}}
`
	u := writeDump(t, "export.json.gz", gzipBytes(t, []byte(export)), "format=dynamodb-json&keys=pk:S,sk:N")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)

	recs := collect(t, st)
	// A composite key renders as a JSON array of the two key attributes in schema order.
	require.Equal(t, query.Record{Key: `["a","3"]`, Type: "item", Value: map[string]any{
		"pk": "a", "sk": 3, "v": "z",
	}}, recs[`["a","3"]`])
	require.Equal(t, map[string]any{"pk": "a", "sk": 10, "v": "w"}, recs[`["a","10"]`].Value)
}

func TestDynamoRequiresKeys(t *testing.T) {
	u := writeDump(t, "scan.json", []byte(`{"Items":[]}`), "format=dynamodb-json")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	err = st.TypedScan(t.Context(), func([]query.Record) error { return nil })
	require.ErrorContains(t, err, "key schema")
}

func TestDynamoUnknownShape(t *testing.T) {
	u := writeDump(t, "bad.json", []byte(`{"Records":[]}`), "format=dynamodb-json&keys=pk")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	err = st.TypedScan(t.Context(), func([]query.Record) error { return nil })
	require.ErrorContains(t, err, "neither")
}

func TestPagingDynamo(t *testing.T) {
	var buf bytes.Buffer
	for i := range bigCount {
		fmt.Fprintf(&buf, `{"Item":{"pk":{"S":"k%d"}}}`+"\n", i)
	}
	u := writeDump(t, "big.json", buf.Bytes(), "format=dynamodb-json&keys=pk")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, []int{pageSize, bigCount - pageSize}, pageSizes(t, st))
}

func TestPagingExactMultipleDynamo(t *testing.T) {
	var buf bytes.Buffer
	for i := range pageSize {
		fmt.Fprintf(&buf, `{"Item":{"pk":{"S":"k%d"}}}`+"\n", i)
	}
	u := writeDump(t, "exact.json", buf.Bytes(), "format=dynamodb-json&keys=pk")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, []int{pageSize}, pageSizes(t, st))
}

func TestDynamoFormatString(t *testing.T) {
	require.Equal(t, "dynamodb-json", FormatDynamoDBJSON.String())
	f, err := ParseFormat("ddb")
	require.NoError(t, err)
	require.Equal(t, FormatDynamoDBJSON, f)
}

// TestDynamoSourceFailures pins the dump reader's error handling: an "Items" value
// that is not an array is a decode failure rather than a silently empty page, a
// malformed top-level object is reported, and a consumer error propagates even when
// a later page would have succeeded.
func TestDynamoSourceFailures(t *testing.T) {
	drain := func(t *testing.T, body string, size int, fn func([]query.Record) error) error {
		t.Helper()
		src, err := dynamoSource(strings.NewReader(body), size, numfmt.DecimalAuto, Hints{Keys: "pk"})
		require.NoError(t, err)
		return src(context.Background(), fn)
	}
	ignore := func([]query.Record) error { return nil }

	t.Run("a malformed element of Items is refused", func(t *testing.T) {
		err := drain(t, `{"Items":[{"pk":5}]}`, pageSize, ignore)
		require.ErrorContains(t, err, `dynamodb: attribute "pk"`)
	})
	t.Run("a malformed Item is refused", func(t *testing.T) {
		err := drain(t, `{"Item":{"pk":5}}`, pageSize, ignore)
		require.ErrorContains(t, err, `dynamodb: attribute "pk"`)
	})
	t.Run("a non-array Items value is a decode failure", func(t *testing.T) {
		err := drain(t, `{"Items":5}`, pageSize, ignore)
		require.ErrorContains(t, err, "decode dynamodb Items")
		requireWrapped(t, err)
	})
	t.Run("a malformed dump is a decode failure", func(t *testing.T) {
		err := drain(t, `{"Item":`, pageSize, ignore)
		require.ErrorContains(t, err, "decode dynamodb dump")
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})
	t.Run("a consumer error propagates from a full page", func(t *testing.T) {
		boom := errors.New("consumer said no")
		f := &failFirstCall{err: boom}
		body := `{"Items":[{"pk":{"S":"a"}},{"pk":{"S":"b"}},{"pk":{"S":"c"}}]}`
		require.ErrorIs(t, drain(t, body, 2, f.accept), boom)
	})
	t.Run("a cancelled scan stops", func(t *testing.T) {
		src, err := dynamoSource(strings.NewReader(`{"Items":[{"pk":{"S":"a"}}]}`), pageSize, numfmt.DecimalAuto, Hints{Keys: "pk"})
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.ErrorIs(t, src(ctx, ignore), context.Canceled)
	})
}

// TestDynamoSourceRejectsABadKeySchema gives a ?keys= hint with three attributes.
// The source refuses it before it reads a record.
func TestDynamoSourceRejectsABadKeySchema(t *testing.T) {
	src, err := dynamoSource(strings.NewReader(`{"Items":[]}`), pageSize, numfmt.DecimalAuto, Hints{Keys: "a,b,c"})
	require.ErrorContains(t, err, "want 1 (partition) or 2 (partition,sort)")
	require.Nil(t, src)
}

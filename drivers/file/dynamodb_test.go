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
	// Native S3 export: NDJSON, one {"Item":{…}} per line, gzipped, composite key.
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

// TestDynamoSourceFailures pins the error handling of the dump reader. An
// "Items" value that is not an array is a decode failure, not a silently empty
// page. A malformed top-level object is reported. A consumer error propagates
// even if a later page can succeed.
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

// countingReader records how many bytes the consumer has read.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// TestDynamoScanOutputStreams proves that the reader emits the first item
// before it reads the whole scan output, so a large table is not held in memory.
func TestDynamoScanOutputStreams(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"Items":[`)
	for i := range 20000 {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"pk":{"S":"k%d"},"pad":{"S":"%s"}}`, i, strings.Repeat("x", 50))
	}
	sb.WriteString(`],"Count":20000,"ScannedCount":20000}`)
	body := sb.String()

	cr := &countingReader{r: strings.NewReader(body)}
	src, err := dynamoSource(cr, 1, numfmt.DecimalAuto, Hints{Keys: "pk"})
	require.NoError(t, err)
	readAtFirst := -1
	require.NoError(t, src(context.Background(), func([]query.Record) error {
		if readAtFirst < 0 {
			readAtFirst = cr.n
		}
		return nil
	}))
	require.Positive(t, readAtFirst)
	require.Less(t, readAtFirst, len(body)/10, "the first item must be emitted long before the input ends")
}

func TestDynamoSourceShapes(t *testing.T) {
	keys := func(recs []query.Record) []string {
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.Key)
		}
		return out
	}
	tests := []struct {
		name    string
		body    string
		want    []string
		wantErr string
	}{
		{"scan output", `{"Items":[{"pk":{"S":"a"}},{"pk":{"S":"b"}}],"Count":2}`, []string{"a", "b"}, ""},
		{"two scan objects", `{"Items":[{"pk":{"S":"a"}}],"Count":1} {"Items":[{"pk":{"S":"b"}}],"Count":1}`, []string{"a", "b"}, ""},
		{"ndjson export", "{\"Item\":{\"pk\":{\"S\":\"a\"}}}\n{\"Item\":{\"pk\":{\"S\":\"b\"}}}\n{\"Item\":{\"pk\":{\"S\":\"c\"}}}\n", []string{"a", "b", "c"}, ""},
		{"extra keys around Items", `{"Count":1,"ConsumedCapacity":{"TableName":"t"},"Items":[{"pk":{"S":"a"}}],"LastEvaluatedKey":{"pk":{"S":"a"}},"ScannedCount":1}`, []string{"a"}, ""},
		{"empty Items", `{"Items":[],"Count":0}`, []string{}, ""},
		{"no Items or Item", `{"Count":1}`, nil, "neither"},
		{"malformed json", `{"Items":[{"pk":{"S":"a"}},`, nil, "decode dynamodb"},
		{"Items not an array", `{"Items":{"pk":{"S":"a"}}}`, nil, "decode dynamodb Items"},
		{"top level not an object", `[1]`, nil, "decode dynamodb dump"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, err := dynamoSource(strings.NewReader(tc.body), pageSize, numfmt.DecimalAuto, Hints{Keys: "pk"})
			require.NoError(t, err)
			var got []query.Record
			err = src(context.Background(), func(b []query.Record) error {
				got = append(got, b...)
				return nil
			})
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, keys(got))
		})
	}
}

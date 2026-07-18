package parquetout

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/stretchr/testify/require"
)

// writeParquet streams the values through a Writer into a buffer.
func writeParquet(t *testing.T, vals []any) []byte {
	t.Helper()
	var buf bytes.Buffer
	pw := NewWriter(&buf)
	for _, v := range vals {
		require.NoError(t, pw.Add(v))
	}
	require.NoError(t, pw.Close())
	return buf.Bytes()
}

// readTable reads a Parquet buffer back into an Arrow table.
func readTable(t *testing.T, b []byte) arrow.Table {
	t.Helper()
	mem := memory.NewGoAllocator()
	tbl, err := pqarrow.ReadTable(
		context.Background(),
		bytes.NewReader(b),
		parquet.NewReaderProperties(mem),
		pqarrow.ArrowReadProperties{},
		mem,
	)
	require.NoError(t, err)
	t.Cleanup(tbl.Release)
	return tbl
}

// oneRecord flattens a table into a single record batch for value assertions.
func oneRecord(t *testing.T, tbl arrow.Table) arrow.RecordBatch {
	t.Helper()
	tr := array.NewTableReader(tbl, tbl.NumRows())
	t.Cleanup(tr.Release)
	require.True(t, tr.Next())
	rec := tr.RecordBatch()
	rec.Retain()
	t.Cleanup(rec.Release)
	return rec
}

// colOf returns a record's column array by field name.
func colOf(t *testing.T, rec arrow.RecordBatch, name string) arrow.Array {
	t.Helper()
	idx := rec.Schema().FieldIndices(name)
	require.Len(t, idx, 1, "column %q missing", name)
	return rec.Column(idx[0])
}

func TestWriterRoundTrip(t *testing.T) {
	ts0 := "2021-01-02T03:04:05.123456789Z"
	vals := []any{
		map[string]any{"n": 1, "ts": ts0, "obj": map[string]any{"a": 10}, "misc": 1},
		map[string]any{"n": 2, "ts": "2022-06-07T08:09:10Z", "obj": map[string]any{"a": 20}, "misc": "hello"},
	}
	tbl := readTable(t, writeParquet(t, vals))
	require.Equal(t, int64(2), tbl.NumRows())

	// Arrow types survive the write-read cycle exactly (WithStoreSchema).
	s := tbl.Schema()
	require.True(t, arrow.TypeEqual(arrow.PrimitiveTypes.Int64, field(t, s, "n").Type))
	require.True(t, arrow.TypeEqual(&arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"}, field(t, s, "ts").Type))
	_, isStruct := field(t, s, "obj").Type.(*arrow.StructType)
	require.True(t, isStruct)
	// The heterogeneous column round-trips as arrow.json (isJSONField reads the
	// read-back schema, so it confirms the iq:extension marker and utf8 storage
	// survived), and its presence metadata survives too.
	miscField := field(t, s, "misc")
	require.True(t, isJSONField(miscField), "misc should round-trip as arrow.json, got %s", miscField.Type)
	require.Equal(t, "required", presenceOf(t, miscField))

	rec := oneRecord(t, tbl)

	nCol := colOf(t, rec, "n").(*array.Int64)
	require.Equal(t, int64(1), nCol.Value(0))
	require.Equal(t, int64(2), nCol.Value(1))

	want0, err := time.Parse(time.RFC3339Nano, ts0)
	require.NoError(t, err)
	tsCol := colOf(t, rec, "ts").(*array.Timestamp)
	require.Equal(t, arrow.Timestamp(want0.UTC().UnixNano()), tsCol.Value(0))

	objCol := colOf(t, rec, "obj").(*array.Struct)
	aCol := objCol.Field(0).(*array.Int64) // struct fields sorted: "a" only
	require.Equal(t, int64(10), aCol.Value(0))
	require.Equal(t, int64(20), aCol.Value(1))

	// arrow.json payloads are byte-equal canonical JSON in plain utf8 storage.
	miscCol := colOf(t, rec, "misc").(*array.String)
	require.Equal(t, "1", miscCol.Value(0))
	require.Equal(t, `"hello"`, miscCol.Value(1))
}

func TestWriterListAndMapRoundTrip(t *testing.T) {
	// Eight rows so "cfg" trips the id-keyed-map heuristic; "tags" is a list.
	vals := make([]any, 0, 8)
	for i := 0; i < 8; i++ {
		vals = append(vals, map[string]any{
			"id":   i,
			"tags": []any{"x", "y"},
			"cfg":  map[string]any{kkey(i, "a"): 1, kkey(i, "b"): 2},
		})
	}
	tbl := readTable(t, writeParquet(t, vals))
	require.Equal(t, int64(8), tbl.NumRows())

	_, isList := field(t, tbl.Schema(), "tags").Type.(*arrow.ListType)
	require.True(t, isList)
	_, isMap := field(t, tbl.Schema(), "cfg").Type.(*arrow.MapType)
	require.True(t, isMap)
}

func TestWriterEmptyStream(t *testing.T) {
	// Closing without any value still yields a readable zero-row file.
	var buf bytes.Buffer
	pw := NewWriter(&buf)
	require.NoError(t, pw.Close())
	tbl := readTable(t, buf.Bytes())
	require.Equal(t, int64(0), tbl.NumRows())
}

func TestWriterPostSampleMismatch(t *testing.T) {
	// Fill the sample with homogeneous integer rows so the schema locks "age" to
	// int64, then feed a string past the sample boundary: it must fail, never
	// coerce. This exercises the real post-sample encode path (Add after start).
	var buf bytes.Buffer
	pw := NewWriter(&buf)
	for i := 0; i < sampleBufferSize; i++ {
		require.NoError(t, pw.Add(map[string]any{"age": 1}))
	}
	err := pw.Add(map[string]any{"age": "thirty"})
	require.Error(t, err)
	require.Contains(t, err.Error(), `"age"`)
	require.Contains(t, err.Error(), "int64")
	require.Contains(t, err.Error(), "jsonl")
}

func TestWriterTimestampParseFailurePropagates(t *testing.T) {
	// The sample sees only valid RFC3339 timestamps, so the column locks to
	// timestamp[ns, UTC]; a malformed timestamp past the sample fails the export
	// rather than coercing to a wrong instant.
	var buf bytes.Buffer
	pw := NewWriter(&buf)
	for i := 0; i < sampleBufferSize; i++ {
		require.NoError(t, pw.Add(map[string]any{"ts": "2021-01-02T03:04:05Z"}))
	}
	err := pw.Add(map[string]any{"ts": "nope"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "RFC3339Nano")
	require.Contains(t, err.Error(), `"ts"`)
}

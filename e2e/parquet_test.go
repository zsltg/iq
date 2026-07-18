package e2e

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/stretchr/testify/require"
)

// TestParquetExportRoundTrip drives the built binary black-box: it exports a
// file-backed source as Parquet, then reads the bytes back in-process and
// asserts the row count and a value survived.
func TestParquetExportRoundTrip(t *testing.T) {
	skipShort(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "data.jsonl")
	require.NoError(t, os.WriteFile(data,
		[]byte(`{"key":"1","value":{"name":"alice","age":30}}`+"\n"+
			`{"key":"2","value":{"name":"bob","age":25}}`+"\n"), 0o600))
	env := []string{"IQ_CONFIG=" + filepath.Join(dir, "iq.toml")}

	_, stderr, code := run(t, env, "add", "file://"+data, "-n", "snap")
	require.Zerof(t, code, "add failed: %s", stderr)

	out, stderr, code := run(t, env, "--src", "snap", ".[]", "--format", "parquet")
	require.Zerof(t, code, "parquet export failed: %s", stderr)
	require.NotEmpty(t, out)

	mem := memory.NewGoAllocator()
	tbl, err := pqarrow.ReadTable(
		context.Background(),
		bytes.NewReader([]byte(out)),
		parquet.NewReaderProperties(mem),
		pqarrow.ArrowReadProperties{},
		mem,
	)
	require.NoError(t, err)
	defer tbl.Release()
	require.Equal(t, int64(2), tbl.NumRows())

	// age is an int64 column; assert both values are present (scan order is not
	// guaranteed for a file source).
	require.True(t, arrow.TypeEqual(arrow.PrimitiveTypes.Int64, colType(t, tbl, "age")))
	ages := intColumn(t, tbl, "age")
	require.ElementsMatch(t, []int64{30, 25}, ages)
}

// colType returns the Arrow type of a named table column.
func colType(t *testing.T, tbl arrow.Table, name string) arrow.DataType {
	t.Helper()
	fs, ok := tbl.Schema().FieldsByName(name)
	require.Truef(t, ok, "column %q missing", name)
	return fs[0].Type
}

// intColumn collects a named int64 column's values across all chunks.
func intColumn(t *testing.T, tbl arrow.Table, name string) []int64 {
	t.Helper()
	idx := tbl.Schema().FieldIndices(name)
	require.Len(t, idx, 1)
	chunked := tbl.Column(idx[0]).Data()
	var vals []int64
	for _, chunk := range chunked.Chunks() {
		arr := chunk.(*array.Int64)
		for i := 0; i < arr.Len(); i++ {
			vals = append(vals, arr.Value(i))
		}
	}
	return vals
}

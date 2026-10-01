package parquetout

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/stretchr/testify/require"
)

// rowGroups reports how many Parquet row groups a buffer holds — one per written
// record batch, so it pins the page-flush cadence.
func rowGroups(t *testing.T, b []byte) int {
	t.Helper()
	r, err := file.NewParquetReader(bytes.NewReader(b))
	require.NoError(t, err)
	defer func() { require.NoError(t, r.Close()) }()
	return r.NumRowGroups()
}

// TestWriterFullRoundTrip writes eight rows spanning every scalar mapping, a
// nested struct, a list, an id-keyed map, a heterogeneous arrow.json column, and
// an optional column, then reads back and asserts the COMPLETE content of every
// column for every row. A single mutated field, dropped loop iteration, or
// wrong enc.kind changes an asserted value, so the whole projection and encoder
// are pinned by observable behaviour.
func TestWriterFullRoundTrip(t *testing.T) {
	const n = 8
	vals := make([]any, 0, n)
	for i := range n {
		row := map[string]any{
			"i":  i,
			"f":  float64(i) + 0.5,
			"b":  i%2 == 0,
			"s":  fmt.Sprintf("s%d", i),
			"d":  fmt.Sprintf("2021-03-%02d", i+1),
			"t":  fmt.Sprintf("2021-03-01T00:00:0%dZ", i),
			"st": map[string]any{"a": i, "b": fmt.Sprintf("b%d", i)},
			"lst": func() []any {
				if i == 0 {
					return []any{} // empty list: the element loop runs zero times
				}
				return []any{i, i + 1}
			}(),
			"cfg": map[string]any{
				fmt.Sprintf("a_%d", i): i + 1,
				fmt.Sprintf("z_%d", i): (i + 1) * 100,
			},
		}
		if i%2 == 0 {
			row["het"] = i     // integer in even rows...
			row["opt"] = i * 7 // ...present only in even rows
			row["narb"] = i    // integer-or-null: present-and-int in even rows...
		} else {
			row["het"] = fmt.Sprintf("h%d", i) // ...string in odd rows → arrow.json
			row["narb"] = nil                  // ...explicit null in odd rows → [integer, null]
		}
		vals = append(vals, row)
	}

	tbl := readTable(t, writeParquet(t, vals))
	require.Equal(t, int64(n), tbl.NumRows())
	rec := oneRecord(t, tbl)

	iCol := colOf(t, rec, "i").(*array.Int64)
	fCol := colOf(t, rec, "f").(*array.Float64)
	bCol := colOf(t, rec, "b").(*array.Boolean)
	sCol := colOf(t, rec, "s").(*array.String)
	dCol := colOf(t, rec, "d").(*array.Date32)
	tCol := colOf(t, rec, "t").(*array.Timestamp)
	stCol := colOf(t, rec, "st")
	lstCol := colOf(t, rec, "lst")
	cfgCol := colOf(t, rec, "cfg")
	hetCol := colOf(t, rec, "het").(*array.String)
	optCol := colOf(t, rec, "opt").(*array.Int64)
	narbCol := colOf(t, rec, "narb").(*array.Int64)

	for i := range n {
		require.Equalf(t, int64(i), iCol.Value(i), "i row %d", i)
		require.InDeltaf(t, float64(i)+0.5, fCol.Value(i), 1e-9, "f row %d", i)
		require.Equalf(t, i%2 == 0, bCol.Value(i), "b row %d", i)
		require.Equalf(t, fmt.Sprintf("s%d", i), sCol.Value(i), "s row %d", i)

		wantDate, err := time.Parse("2006-01-02", fmt.Sprintf("2021-03-%02d", i+1))
		require.NoError(t, err)
		require.Equalf(t, arrow.Date32FromTime(wantDate), dCol.Value(i), "d row %d", i)

		wantTS, err := time.Parse(time.RFC3339Nano, fmt.Sprintf("2021-03-01T00:00:0%dZ", i))
		require.NoError(t, err)
		require.Equalf(t, arrow.Timestamp(wantTS.UTC().UnixNano()), tCol.Value(i), "t row %d", i)

		// Nested struct: BOTH fields, every row.
		require.JSONEqf(t, fmt.Sprintf(`{"a":%d,"b":"b%d"}`, i, i), stCol.ValueStr(i), "st row %d", i)

		// List: empty for row 0, both elements otherwise.
		if i == 0 {
			require.Equal(t, "[]", lstCol.ValueStr(0), "empty list row 0")
		} else {
			require.Equalf(t, fmt.Sprintf("[%d,%d]", i, i+1), lstCol.ValueStr(i), "lst row %d", i)
		}

		// Map: both entries, keys sorted (a_ before z_). JSONEq keeps array order,
		// so a dropped or reordered entry still fails.
		require.JSONEqf(t,
			fmt.Sprintf(`[{"key":"a_%d","value":%d},{"key":"z_%d","value":%d}]`, i, i+1, i, (i+1)*100),
			cfgCol.ValueStr(i), "cfg row %d", i)

		// arrow.json fallback: integer text in even rows, quoted string in odd.
		if i%2 == 0 {
			require.Equalf(t, fmt.Sprintf("%d", i), hetCol.Value(i), "het row %d", i)
			require.Equalf(t, int64(i*7), optCol.Value(i), "opt row %d", i)
			require.Falsef(t, narbCol.IsNull(i), "narb should be set in even row %d", i)
			require.Equalf(t, int64(i), narbCol.Value(i), "narb row %d", i)
		} else {
			require.Equalf(t, fmt.Sprintf(`"h%d"`, i), hetCol.Value(i), "het row %d", i)
			require.Truef(t, optCol.IsNull(i), "opt should be null in odd row %d", i)
			require.Truef(t, narbCol.IsNull(i), "narb should be null in odd row %d", i)
		}
	}

	// The full schema — every column's type, nullability, presence, and the
	// arrow.json marker — survives the round-trip exactly.
	assertColumn(t, tbl.Schema(), "i", arrow.PrimitiveTypes.Int64, "required", false)
	assertColumn(t, tbl.Schema(), "f", arrow.PrimitiveTypes.Float64, "required", false)
	assertColumn(t, tbl.Schema(), "b", arrow.FixedWidthTypes.Boolean, "required", false)
	assertColumn(t, tbl.Schema(), "s", arrow.BinaryTypes.String, "required", false)
	assertColumn(t, tbl.Schema(), "d", arrow.FixedWidthTypes.Date32, "required", false)
	assertColumn(t, tbl.Schema(), "t", &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"}, "required", false)
	assertColumn(t, tbl.Schema(), "het", arrow.BinaryTypes.String, "required", true)
	assertColumn(t, tbl.Schema(), "opt", arrow.PrimitiveTypes.Int64, "optional", false)
	// narb is integer-or-null: "null" is stripped so it stays a nullable int64,
	// never the arrow.json fallback.
	assertColumn(t, tbl.Schema(), "narb", arrow.PrimitiveTypes.Int64, "required", false)

	// Columns come out in sorted name order (objectType sorts the field names);
	// with a dozen columns a lost sort would almost never land sorted by chance.
	names := make([]string, tbl.Schema().NumFields())
	for i, f := range tbl.Schema().Fields() {
		names[i] = f.Name
	}
	require.Equal(t, []string{"b", "cfg", "d", "f", "het", "i", "lst", "narb", "opt", "s", "st", "t"}, names)
}

// assertColumn pins one column's type, nullability, presence metadata, and
// whether it carries the arrow.json extension marker.
func assertColumn(t *testing.T, s *arrow.Schema, name string, dt arrow.DataType, presence string, isJSON bool) {
	t.Helper()
	f := field(t, s, name)
	require.Truef(t, arrow.TypeEqual(dt, f.Type), "column %q type: want %s got %s", name, dt, f.Type)
	require.Truef(t, f.Nullable, "column %q should be nullable", name)
	require.Equalf(t, presence, presenceOf(t, f), "column %q presence", name)
	require.Equalf(t, isJSON, isJSONField(f), "column %q arrow.json marker", name)
}

// TestWriterMapKeysSorted pins appendMap's sort.Strings: a map column with ten
// keys per row must serialize with keys in ascending order. Go randomizes map
// iteration, so dropping the sort yields a random order that matches the sorted
// expectation only 1/10! of the time — the assertion is order-sensitive (a JSON
// array), so a lost sort fails with overwhelming probability.
func TestWriterMapKeysSorted(t *testing.T) {
	const rows, keysPerRow = 8, 10
	vals := make([]any, 0, rows)
	for i := range rows {
		cfg := map[string]any{}
		for j := range keysPerRow {
			// Per-(key,row)-unique keys so the object is detected as an id-keyed map.
			cfg[fmt.Sprintf("k%02d_%d", j, i)] = j
		}
		vals = append(vals, map[string]any{"id": i, "cfg": cfg})
	}

	tbl := readTable(t, writeParquet(t, vals))
	rec := oneRecord(t, tbl)
	cfgCol := colOf(t, rec, "cfg")
	_, isMap := field(t, tbl.Schema(), "cfg").Type.(*arrow.MapType)
	require.True(t, isMap, "cfg should be a map")

	// Row 0's entries, keys in sorted (k00 < k01 < ... < k09) order.
	parts := make([]string, keysPerRow)
	for j := range keysPerRow {
		parts[j] = fmt.Sprintf(`{"key":"k%02d_0","value":%d}`, j, j)
	}
	want := "[" + strings.Join(parts, ",") + "]"
	require.JSONEq(t, want, cfgCol.ValueStr(0), "map entries must serialize with keys sorted")
}

// TestWriterSingleRow pins the final single-row page: the flushPage empty-guard
// (rows == 0) must not swallow a page of exactly one row.
func TestWriterSingleRow(t *testing.T) {
	tbl := readTable(t, writeParquet(t, []any{map[string]any{"n": 42}}))
	require.Equal(t, int64(1), tbl.NumRows())
	rec := oneRecord(t, tbl)
	require.Equal(t, int64(42), colOf(t, rec, "n").(*array.Int64).Value(0))
}

// TestWriterCrossesPageBoundary pins the flush cadence at two page sizes so every
// counter mutation is observable: a full page plus a one-row tail (pageBatchSize
// +1) and two full pages (2*pageBatchSize) both yield exactly two row groups with
// every row present. Flushing every row, never mid-stream, resetting the counter
// to one (boundary drifts high) or minus one (drops the tail row) all change the
// row-group count or lose a row.
func TestWriterCrossesPageBoundary(t *testing.T) {
	for _, total := range []int{pageBatchSize + 1, 2 * pageBatchSize} {
		t.Run(fmt.Sprintf("%d rows", total), func(t *testing.T) {
			vals := make([]any, 0, total)
			for i := range total {
				vals = append(vals, map[string]any{"n": i})
			}
			buf := writeParquet(t, vals)
			require.Equal(t, 2, rowGroups(t, buf))

			tbl := readTable(t, buf)
			require.Equal(t, int64(total), tbl.NumRows())
			rec := oneRecord(t, tbl)
			nCol := colOf(t, rec, "n").(*array.Int64)
			for i := range total {
				require.Equalf(t, int64(i), nCol.Value(i), "row %d", i)
			}
		})
	}
}

// TestWriterPageSize pins the page size to 500 rows with literal counts. The
// test above reads the page size from pageBatchSize, so it cannot see a change
// of the constant itself.
func TestWriterPageSize(t *testing.T) {
	tests := []struct {
		rows      int
		rowGroups int
	}{
		{500, 1},
		{501, 2},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d rows", tt.rows), func(t *testing.T) {
			vals := make([]any, 0, tt.rows)
			for i := range tt.rows {
				vals = append(vals, map[string]any{"n": i})
			}
			require.Equal(t, tt.rowGroups, rowGroups(t, writeParquet(t, vals)))
		})
	}
}

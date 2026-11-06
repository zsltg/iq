package hbase

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// b64 is the standard base64 text of s.
func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// collectNative runs NativeScan and returns every page it handed to fn.
func collectNative(ctx context.Context, st *Store) ([][]query.Record, error) {
	var pages [][]query.Record
	err := st.NativeScan(ctx, func(batch []query.Record) error {
		pages = append(pages, batch)
		return nil
	})
	return pages, err
}

// rawFake returns a store over a fake that holds rows with bytes that are not text.
func rawFake(t *testing.T) (*Store, *fakeClient) {
	t.Helper()
	fc := newFakeClient()
	fc.seed("\xff\x00key", map[string]map[string][]byte{
		"cf":    {"\xfe\x01": []byte("\x80\x81"), "name": []byte("Dune"), "empty": {}},
		"extra": {"q": []byte{0}},
	})
	return newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto), fc
}

func TestNativeFormNames(t *testing.T) {
	st := &Store{}
	require.Equal(t, query.FormHBaseRaw, st.NativeForm())
	require.Equal(t, query.FormHBaseRaw, st.WritesForm())
	var _ query.NativeReader = st
	var _ query.FormWriter = st
}

func TestNativeScanBuildsRawRecords(t *testing.T) {
	st, _ := rawFake(t)

	pages, err := collectNative(context.Background(), st)

	require.NoError(t, err)
	require.Equal(t, [][]query.Record{{{
		Key:  b64("\xff\x00key"),
		Type: "row",
		Form: query.FormHBaseRaw,
		Value: map[string]any{
			"cf": map[string]any{
				b64("\xfe\x01"): b64("\x80\x81"),
				b64("name"):     b64("Dune"),
				b64("empty"):    "",
			},
			"extra": map[string]any{b64("q"): "AA=="},
		},
	}}}, pages)
}

func TestNativeScanIgnoresDeclaredTypes(t *testing.T) {
	tests := []struct {
		name  string
		types typeMap
		key   colType
	}{
		{"column types", typeMap{cellKey("cf", "name"): ctLong, cellKey("cf", "empty"): ctBytes}, ctAuto},
		{"row key type", typeMap{}, ctLong},
		{"both", typeMap{cellKey("cf", "name"): ctInt}, ctBytes},
	}
	plain, _ := rawFake(t)
	want, err := collectNative(context.Background(), plain)
	require.NoError(t, err)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, fc := rawFake(t)
			st.types, st.rowkeyType = tt.types, tt.key

			got, err := collectNative(context.Background(), st)

			require.NoError(t, err)
			require.Equal(t, want, got)
			require.Len(t, fc.scans, 1)
		})
	}
}

func TestNativeScanPages(t *testing.T) {
	tests := []struct {
		name  string
		rows  int
		pages []int
	}{
		{"no rows", 0, nil},
		{"one row", 1, []int{1}},
		{"exactly one page", 2, []int{2}},
		{"one page and a partial page", 3, []int{2, 1}},
		{"exactly two pages", 4, []int{2, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, fc := rawFake(t)
			fc.rows = map[string]map[string]map[string][]byte{}
			for i := range tt.rows {
				fc.seed(fmt.Sprintf("r%d", i), cell("cf", "q", "v"))
			}

			pages, err := collectNative(context.Background(), st)

			require.NoError(t, err)
			var sizes []int
			var keys []string
			for _, p := range pages {
				sizes = append(sizes, len(p))
				for _, r := range p {
					keys = append(keys, r.Key)
				}
			}
			require.Equal(t, tt.pages, sizes)
			for i, k := range keys {
				require.Equal(t, b64(fmt.Sprintf("r%d", i)), k)
			}
			require.Len(t, keys, tt.rows)
		})
	}
}

func TestNativeScanKeepsEachPage(t *testing.T) {
	st, fc := rawFake(t)
	fc.seed("a", cell("cf", "q", "1"))
	fc.seed("b", cell("cf", "q", "2"))
	fc.seed("c", cell("cf", "q", "3"))
	var kept [][]query.Record
	require.NoError(t, st.NativeScan(context.Background(), func(batch []query.Record) error {
		kept = append(kept, batch)
		return nil
	}))
	require.Equal(t, b64("a"), kept[0][0].Key)
	require.Equal(t, b64("b"), kept[0][1].Key)
}

func TestNativeScanRequest(t *testing.T) {
	st, fc := cappedStore(2)

	_, err := collectNative(context.Background(), st)

	require.NoError(t, err)
	require.Len(t, fc.scans, 1)
	requireTestCtxFree(t, fc.scans[0])
	require.Equal(t, "books", fc.scans[0].table)
	require.Equal(t, uint32(7), storeLimitOf(t, fc))
	requireDefaultMaxVersions(t, scanPB(t, fc.scans[0]).MaxVersions)
	require.Nil(t, scanPB(t, fc.scans[0]).GetFilter(), "a native read pushes no filter")
}

// requireTestCtxFree requires only that the request carries a context.
func requireTestCtxFree(t *testing.T, rec rpcCall) {
	t.Helper()
	require.NotNil(t, rec.ctx)
}

func TestNativeScanRequestBindsTheContext(t *testing.T) {
	st, fc := cappedStore(1)

	require.NoError(t, st.NativeScan(testCtx(), func([]query.Record) error { return nil }))

	requireTestCtx(t, fc.scans[0].ctx)
}

func TestNativeScanUnboundedSetsNoCap(t *testing.T) {
	st, fc := cappedStore(2)

	_, err := collectNative(query.WithUnbounded(context.Background()), st)

	require.NoError(t, err)
	require.Zero(t, storeLimitOf(t, fc))
}

func TestNativeScanRowCap(t *testing.T) {
	tests := []struct {
		name      string
		cells     int
		unbounded bool
		refused   bool
	}{
		{"at the limit", 6, false, false},
		{"one over", 7, false, true},
		{"unbounded reads the large row", 50, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _ := cappedStore(tt.cells)
			ctx := context.Background()
			if tt.unbounded {
				ctx = query.WithUnbounded(ctx)
			}

			_, err := collectNative(ctx, st)

			if !tt.refused {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrRowTooLarge)
			require.EqualError(t, err, fmt.Sprintf(
				"read row %q: too many cells: a column family has more than 6 cells, so read it with --unbounded", b64("k1")))
		})
	}
}

func TestNativeScanErrors(t *testing.T) {
	tests := []struct {
		name       string
		rows       int
		scanErr    error
		failAt     int
		fn         func([]query.Record) error
		wantErr    error
		wantText   string
		wantCloses int
	}{
		{"first Next fails", 2, errBackend, 0, nil, errBackend, "hbase scan: ", 1},
		{"Next fails after rows", 3, errBackend, 1, nil, errBackend, "hbase scan: ", 1},
		{"fn fails on a full page", 4, nil, 0, func([]query.Record) error { return errStop }, errStop, "", 1},
		{"fn fails on the final page", 1, nil, 0, func([]query.Record) error { return errStop }, errStop, "", 0},
		{"full scan closes nothing", 3, nil, 0, nil, nil, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, fc := rawFake(t)
			fc.rows = map[string]map[string]map[string][]byte{}
			for i := range tt.rows {
				fc.seed(fmt.Sprintf("r%d", i), cell("cf", "q", "v"))
			}
			fc.scanErr, fc.scanFailAt = tt.scanErr, tt.failAt
			fn := tt.fn
			if fn == nil {
				fn = func([]query.Record) error { return nil }
			}

			err := st.NativeScan(context.Background(), fn)

			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
				if errors.Is(tt.wantErr, errStop) {
					require.Equal(t, errStop, err, "an fn error returns as it is")
				}
				if tt.wantText != "" {
					require.ErrorContains(t, err, tt.wantText)
					require.Equal(t, tt.wantErr, errors.Unwrap(err))
				}
			}
			require.Equal(t, tt.wantCloses, fc.scanners[0].closes)
		})
	}
}

func TestNativeScanNeedsATable(t *testing.T) {
	st, _ := rawFake(t)
	st.table = ""

	_, err := collectNative(context.Background(), st)

	require.ErrorIs(t, err, errNoTable)
}

func TestNativeScanRejectsAnOversizedCapRequest(t *testing.T) {
	st, _ := cappedStore(2)
	st.maxRowCells = 1<<31 - 1

	err := st.NativeScan(context.Background(), func([]query.Record) error { return nil })

	require.EqualError(t, err, "hbase scan: 'MaxResultsPerColumnFamily' exceeds supported number of value results")
	require.EqualError(t, errors.Unwrap(err), "'MaxResultsPerColumnFamily' exceeds supported number of value results")
}

func TestNativeScanTrace(t *testing.T) {
	st, _ := rawFake(t)
	var trace bytes.Buffer
	st.trace = &trace

	_, err := collectNative(context.Background(), st)

	require.NoError(t, err)
	require.Equal(t, "hbase> native scan books\n", trace.String())
}

// TestNativeRoundTripWritesIdenticalBytes reads rows in the raw form and writes
// them into another table. Every byte of the key, qualifier, and value must match.
func TestNativeRoundTripWritesIdenticalBytes(t *testing.T) {
	for _, mode := range []query.WriteMode{query.Upsert, query.InsertOnly} {
		t.Run(fmt.Sprintf("mode %d", mode), func(t *testing.T) {
			src, _ := rawFake(t)
			dstClient := newFakeClient()
			dst := newFakeStore(dstClient, &fakeAdmin{}, "copy", typeMap{}, ctLong)
			dst.types = typeMap{cellKey("cf", "name"): ctLong}

			var written query.WriteStat
			require.NoError(t, src.NativeScan(context.Background(), func(batch []query.Record) error {
				stat, err := dst.Put(context.Background(), batch, mode)
				written.Written += stat.Written
				return err
			}))

			require.Equal(t, 1, written.Written)
			require.Equal(t, src.client.(*fakeClient).rows, dstClient.rows)
		})
	}
}

func TestRawPutWritesTheExactBytes(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	rec := query.Record{Key: b64("\x00\xffk"), Form: query.FormHBaseRaw, Value: map[string]any{
		"cf":    map[string]any{b64("\xfe"): b64("\x80"), "": ""},
		"empty": map[string]any{},
	}}

	stat, err := st.Put(context.Background(), []query.Record{rec}, query.Upsert)

	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1}, stat)
	require.Equal(t, map[string]map[string]map[string][]byte{
		"\x00\xffk": {"cf": {"\xfe": []byte("\x80"), "": {}}},
	}, fc.rows)
}

func TestRawPutKeepsMergeAndInsertOnlySemantics(t *testing.T) {
	fc := newFakeClient()
	fc.seed("k", cell("cf", "old", "keep"))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	raw := func(q, v string) query.Record {
		return query.Record{Key: b64("k"), Form: query.FormHBaseRaw, Value: map[string]any{"cf": map[string]any{b64(q): b64(v)}}}
	}

	stat, err := st.Put(context.Background(), []query.Record{raw("new", "1")}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Overwritten: 1}, stat)
	require.Equal(t, map[string][]byte{"old": []byte("keep"), "new": []byte("1")}, fc.rows["k"]["cf"], "an upsert merges cells")

	stat, err = st.Put(context.Background(), []query.Record{raw("new", "2")}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Skipped: 1}, stat)
	require.Equal(t, []byte("1"), fc.rows["k"]["cf"]["new"], "an insert-only write skips a guard cell that exists")
	require.Equal(t, 1, fc.casCalls)
}

func TestRawPutRefusals(t *testing.T) {
	good := map[string]any{"cf": map[string]any{b64("q"): b64("v")}}
	tests := []struct {
		name string
		rec  query.Record
		want string
	}{
		{
			"bad base64 key",
			query.Record{Key: "!!", Value: good},
			`hbase: record "!!": the key is not valid base64: illegal base64 data at input byte 0`,
		},
		{
			"unpadded key",
			query.Record{Key: "YQ", Value: good},
			`hbase: record "YQ": the key is not valid base64: illegal base64 data at input byte 0`,
		},
		{
			"key with a line break",
			query.Record{Key: "YQ==\n", Value: good},
			`hbase: record "YQ==\n": the key is not valid base64`,
		},
		{
			"key with trailing bits",
			query.Record{Key: "YR==", Value: good},
			`hbase: record "YR==": the key is not valid base64`,
		},
		{"empty key", query.Record{Key: "", Value: good}, `hbase: record "": the row key is empty`},
		{
			"bad base64 qualifier",
			query.Record{Key: b64("k"), Value: map[string]any{"cf": map[string]any{"%": b64("v")}}},
			fmt.Sprintf(`hbase: record %q: family "cf": qualifier "%%" is not valid base64: illegal base64 data at input byte 0`, b64("k")),
		},
		{
			"bad base64 value",
			query.Record{Key: b64("k"), Value: map[string]any{"cf": map[string]any{b64("q"): "%"}}},
			fmt.Sprintf(`hbase: record %q: cell "cf:%s": the value is not valid base64: illegal base64 data at input byte 0`, b64("k"), b64("q")),
		},
		{
			"non-string value",
			query.Record{Key: b64("k"), Value: map[string]any{"cf": map[string]any{b64("q"): 7}}},
			fmt.Sprintf(`hbase: record %q: cell "cf:%s": the value must be a base64 string, got int`, b64("k"), b64("q")),
		},
		{
			"null value",
			query.Record{Key: b64("k"), Value: map[string]any{"cf": map[string]any{b64("q"): nil}}},
			fmt.Sprintf(`hbase: record %q: cell "cf:%s": the value must be a base64 string, got <nil>`, b64("k"), b64("q")),
		},
		{
			"non-object family",
			query.Record{Key: b64("k"), Value: map[string]any{"cf": "x"}},
			fmt.Sprintf(`hbase: record %q: family "cf" must map qualifiers to values, got string`, b64("k")),
		},
		{
			"empty family name",
			query.Record{Key: b64("k"), Value: map[string]any{"": map[string]any{b64("q"): b64("v")}}},
			fmt.Sprintf(`hbase: record %q: a family name is empty`, b64("k")),
		},
		{
			"non-object value",
			query.Record{Key: b64("k"), Value: "x"},
			fmt.Sprintf(`hbase: record %q: the value must be a {family: {qualifier: value}} object, got string`, b64("k")),
		},
		{
			"no cells",
			query.Record{Key: b64("k"), Value: map[string]any{"cf": map[string]any{}}},
			fmt.Sprintf(`hbase: record %q has no cells to write`, b64("k")),
		},
	}
	for _, tt := range tests {
		for _, mode := range []query.WriteMode{query.Upsert, query.InsertOnly} {
			t.Run(fmt.Sprintf("%s mode %d", tt.name, mode), func(t *testing.T) {
				fc := newFakeClient()
				st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
				tt.rec.Form = query.FormHBaseRaw

				stat, err := st.Put(context.Background(), []query.Record{tt.rec}, mode)

				require.ErrorContains(t, err, tt.want)
				require.Equal(t, query.WriteStat{}, stat)
				require.Empty(t, fc.rows)
				require.Zero(t, fc.putCalls+fc.casCalls)
			})
		}
	}
}

func TestRawPutNoCellsCause(t *testing.T) {
	st := newFakeStore(newFakeClient(), &fakeAdmin{}, "books", typeMap{}, ctAuto)
	rec := query.Record{Key: b64("k"), Form: query.FormHBaseRaw, Value: map[string]any{}}

	_, err := st.Put(context.Background(), []query.Record{rec}, query.Upsert)

	require.ErrorIs(t, err, errRawNoCells)
}

func TestRawPutWrapsBase64Errors(t *testing.T) {
	st := newFakeStore(newFakeClient(), &fakeAdmin{}, "books", typeMap{}, ctAuto)
	var corrupt base64.CorruptInputError
	tests := []struct {
		name string
		rec  query.Record
	}{
		{"key", query.Record{Key: "!", Value: map[string]any{"cf": map[string]any{b64("q"): b64("v")}}}},
		{"qualifier", query.Record{Key: b64("k"), Value: map[string]any{"cf": map[string]any{"!": b64("v")}}}},
		{"value", query.Record{Key: b64("k"), Value: map[string]any{"cf": map[string]any{b64("q"): "!"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.rec.Form = query.FormHBaseRaw

			_, err := st.Put(context.Background(), []query.Record{tt.rec}, query.Upsert)

			require.ErrorAs(t, err, &corrupt)
		})
	}
}

func TestRawPutRefusesTheBatchBeforeAnyWrite(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	good := query.Record{Key: b64("a"), Form: query.FormHBaseRaw, Value: map[string]any{"cf": map[string]any{b64("q"): b64("v")}}}
	bad := query.Record{Key: "!", Form: query.FormHBaseRaw, Value: good.Value}
	wrong := query.Record{Key: "w", Form: query.FormDynamoJSON, Value: good.Value}

	_, err := st.Put(context.Background(), []query.Record{good, wrong}, query.Upsert)
	require.ErrorContains(t, err, `hbase: record "w" has form "dynamodb-json", but this destination accepts only forms "json" and "hbase-raw"`)
	require.Empty(t, fc.rows, "a refused form writes nothing")

	_, err = st.Put(context.Background(), []query.Record{good, bad}, query.Upsert)
	require.ErrorContains(t, err, `record "!"`)
	require.Len(t, fc.rows, 1, "a row before the bad row is written, as on the normalized path")
}

func TestRawPutIgnoresDeclaredTypes(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{cellKey("cf", "q"): ctLong}, ctLong)
	rec := query.Record{Key: b64("k"), Form: query.FormHBaseRaw, Value: map[string]any{"cf": map[string]any{b64("q"): b64("v")}}}

	_, err := st.Put(context.Background(), []query.Record{rec}, query.Upsert)

	require.NoError(t, err)
	require.Equal(t, map[string][]byte{"q": []byte("v")}, fc.rows["k"]["cf"])
}

func TestPutStillWritesNormalizedRecords(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	rec := query.Record{Key: "k", Value: map[string]any{"cf": map[string]any{"q": "v"}}}

	_, err := st.Put(context.Background(), []query.Record{rec}, query.Upsert)

	require.NoError(t, err)
	require.Equal(t, []byte("v"), fc.rows["k"]["cf"]["q"])
}

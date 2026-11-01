package hbase

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

type noticeCall struct{ key, field string }

// noticeCtx returns a context whose notice sink appends to calls.
func noticeCtx(calls *[]noticeCall) context.Context {
	return query.WithNonFiniteNotice(testCtx(), func(key, field string) {
		*calls = append(*calls, noticeCall{key, field})
	})
}

func TestDecodeCellConvertsNonFiniteDouble(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want any
	}{
		{"nan", math.NaN(), "NaN"},
		{"positive infinity", math.Inf(1), "Infinity"},
		{"negative infinity", math.Inf(-1), "-Infinity"},
		{"finite", 1.5, 1.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := encodeCell(ctDouble, tt.in)
			require.NoError(t, err)

			require.Equal(t, tt.want, decodeCell(ctDouble, raw, nil))
		})
	}
}

func TestNonFiniteDoubleRoundTrips(t *testing.T) {
	for _, in := range []float64{math.Inf(1), math.Inf(-1)} {
		raw, err := encodeCell(ctDouble, in)
		require.NoError(t, err)
		text := decodeCell(ctDouble, raw, nil)

		back, err := encodeCell(ctDouble, text)

		require.NoError(t, err)
		require.Equal(t, raw, back)
	}
}

func TestRowFromCellsRecordsNonFiniteFields(t *testing.T) {
	raw, err := encodeCell(ctDouble, math.Inf(1))
	require.NoError(t, err)
	fc := newFakeClient()
	fc.seed("r1", cell("cf", "price", string(raw)))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{cellKey("cf", "price"): ctDouble}, ctAuto)
	var calls []noticeCall

	got, err := st.Get(noticeCtx(&calls), []string{"r1"})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"r1": map[string]any{"cf": map[string]any{"price": "Infinity"}}}, got)
	require.Equal(t, []noticeCall{{"r1", "cf.price"}}, calls)
}

func TestScanBatchesReportsNonFiniteDouble(t *testing.T) {
	raw, err := encodeCell(ctDouble, math.NaN())
	require.NoError(t, err)
	fc := newFakeClient()
	fc.seed("r1", cell("cf", "price", string(raw)))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{cellKey("cf", "price"): ctDouble}, ctAuto)
	var calls []noticeCall
	var seen map[string]any

	err = st.ScanBatches(noticeCtx(&calls), func(batch map[string]any) error {
		seen = batch
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"r1": map[string]any{"cf": map[string]any{"price": "NaN"}}}, seen)
	require.Equal(t, []noticeCall{{"r1", "cf.price"}}, calls)
}

func TestRowFromCellsReportsEachCellPath(t *testing.T) {
	raw, err := encodeCell(ctDouble, math.Inf(1))
	require.NoError(t, err)
	fc := newFakeClient()
	fc.seed("r1", map[string]map[string][]byte{
		"cf": {"a": raw, "b": raw},
		"cg": {"c": raw},
	})
	types := typeMap{
		cellKey("cf", "a"): ctDouble,
		cellKey("cf", "b"): ctDouble,
		cellKey("cg", "c"): ctDouble,
	}
	st := newFakeStore(fc, &fakeAdmin{}, "books", types, ctAuto)
	var calls []noticeCall

	_, err = st.Get(noticeCtx(&calls), []string{"r1"})

	require.NoError(t, err)
	require.ElementsMatch(t, []noticeCall{{"r1", "cf.a"}, {"r1", "cf.b"}, {"r1", "cg.c"}}, calls)
}

func TestScanReportsRowKeysShownAsBase64(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		rk   colType
		exec bool
		want []string
	}{
		{"scan, valid UTF-8 key", []string{"r1"}, ctAuto, false, nil},
		{"scan, key that is not UTF-8", []string{"r1", "\xff"}, ctAuto, false, []string{"/w=="}},
		{"scan, declared bytes key", []string{"\xff"}, ctBytes, false, nil},
		{"exec scan, key that is not UTF-8", []string{"\xff"}, ctAuto, true, []string{"/w=="}},
		{"exec scan, valid UTF-8 key", []string{"r1"}, ctAuto, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClient()
			for _, k := range tt.keys {
				fc.seed(k, cell("cf", "title", "Dune"))
			}
			st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, tt.rk)
			var shown []string
			ctx := query.WithRowKeyNotice(testCtx(), func(s string) { shown = append(shown, s) })

			var err error
			if tt.exec {
				_, err = st.Query(ctx, []string{"scan", "books"})
			} else {
				err = st.ScanBatches(ctx, func(map[string]any) error { return nil })
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, shown)
		})
	}
}

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

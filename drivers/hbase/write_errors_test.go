package hbase

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// TestInsertOnlyReportsEachRowOutcome pins the outcome of one row at a time. A batch
// of one skipped and one written row hides a swapped outcome, because the two counts
// add up the same.
func TestInsertOnlyReportsEachRowOutcome(t *testing.T) {
	tests := []struct {
		name   string
		seeded bool
		want   query.WriteStat
	}{
		{"a new row is written", false, query.WriteStat{Written: 1}},
		{"an existing row is skipped", true, query.WriteStat{Skipped: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClient()
			if tt.seeded {
				fc.seed("1", cell("cf", "title", "Old"))
			}
			st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

			stat, err := st.Put(testCtx(), []query.Record{rowRecord("1")}, query.InsertOnly)

			require.NoError(t, err)
			require.Equal(t, tt.want, stat)
		})
	}
}

// TestInsertOnlyCheckAndPutFailureIsReturned requires the failure of the guarded
// write to stop the batch with its wrap text and its cause.
func TestInsertOnlyCheckAndPutFailureIsReturned(t *testing.T) {
	fc := newFakeClient()
	fc.casErr = errBackend
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	stat, err := st.Put(testCtx(), []query.Record{rowRecord("1"), rowRecord("2")}, query.InsertOnly)

	require.ErrorContains(t, err, "hbase check-and-put")
	require.ErrorIs(t, err, errBackend)
	require.Equal(t, query.WriteStat{}, stat)
	require.Equal(t, 1, fc.casCalls, "the batch went on after the failure")
	require.Zero(t, fc.putCalls)
}

// TestPutRejectsABadRecordBeforeAnyRequest requires a record that cannot be encoded
// to fail the write with the encoder message and to send no request.
func TestPutRejectsABadRecordBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
		mode  query.WriteMode
	}{
		{"a family that is not an object, upsert", map[string]any{"cf": "scalar"}, `family "cf" must map qualifiers to values`, query.Upsert},
		{"a family that is not an object, insert-only", map[string]any{"cf": "scalar"}, `family "cf" must map qualifiers to values`, query.InsertOnly},
		{"a cell value of the wrong type, upsert", map[string]any{"cf": map[string]any{"title": 7}}, "expected a string value", query.Upsert},
		{"a cell value of the wrong type, insert-only", map[string]any{"cf": map[string]any{"title": 7}}, "expected a string value", query.InsertOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClient()
			st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

			_, err := st.Put(testCtx(), []query.Record{{Key: "1", Value: tt.value}}, tt.mode)

			require.ErrorContains(t, err, tt.want)
			require.Zero(t, fc.putCalls+fc.casCalls+len(fc.gets))
		})
	}
}

// TestScanLimit pins the limit that `scan <table> [limit]` reads, and the error for a
// limit that is not a number.
func TestScanLimit(t *testing.T) {
	t.Run("an absent limit is zero", func(t *testing.T) {
		n, err := scanLimit([]string{"books"})

		require.NoError(t, err)
		require.Zero(t, n)
	})

	t.Run("a limit that is not a number is an error before any scan", func(t *testing.T) {
		fc := newFakeClient()
		st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

		_, err := st.Query(testCtx(), []string{"scan", "books", "many"})

		require.ErrorContains(t, err, `scan limit "many" is not a number`)
		require.Empty(t, fc.scans)
	})
}

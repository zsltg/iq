package hbase

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

func TestOpenRejectsAURLThatDoesNotParse(t *testing.T) {
	// The deadline is short so that a driver that went on to connect would fail on the
	// deadline, not on the URL.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	_, err := Open(ctx, "mysql://host/", "", nil, numfmt.DecimalAuto)
	require.ErrorContains(t, err, "must start with hbase://")
}

func TestVerifyTableSurfacesTheListingError(t *testing.T) {
	st := newFakeStore(newFakeClient(), &fakeAdmin{listErr: errBackend}, "", typeMap{}, ctAuto)
	err := st.verifyTable(context.Background(), "books")
	require.ErrorIs(t, err, errBackend)
}

func TestInsertOnlySurfacesTheCheckAndPutError(t *testing.T) {
	fc := newFakeClient()
	fc.casErr = errBackend
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	_, err := st.Put(context.Background(), []query.Record{rowRecord("1")}, query.InsertOnly)
	require.ErrorContains(t, err, "hbase check-and-put")
	require.ErrorIs(t, err, errBackend)
}

func TestPutRejectsACellThatDoesNotEncodeBeforeAnyWrite(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{cellKey("cf", "n"): ctInt}, ctAuto)
	rec := query.Record{Key: "1", Type: "row", Value: map[string]any{"cf": map[string]any{"n": "abc"}}}
	_, err := st.Put(context.Background(), []query.Record{rec}, query.Upsert)
	require.Error(t, err)
	require.Zero(t, fc.putCalls+fc.casCalls)
}

func TestExecPutRejectsAValueThatDoesNotEncode(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{cellKey("cf", "n"): ctInt}, ctAuto)
	_, err := st.Query(context.Background(), []string{"put", "books", "1", "cf:n", "abc"})
	require.Error(t, err)
	require.Zero(t, fc.putCalls)
}

func TestScansSkipAnEmptyResult(t *testing.T) {
	// A row with no cells yields a result with no cells. The scan loops must pass over
	// it rather than read the row key from a cell that is not there.
	seed := func() *fakeClient {
		fc := newFakeClient()
		fc.seed("1", cell("cf", "title", "Dune"))
		fc.seed("2", map[string]map[string][]byte{})
		fc.seed("3", cell("cf", "title", "Hyperion"))
		return fc
	}
	t.Run("streaming scan", func(t *testing.T) {
		st := newFakeStore(seed(), &fakeAdmin{}, "books", typeMap{}, ctAuto)
		got := map[string]any{}
		err := st.ScanBatches(context.Background(), func(batch map[string]any) error {
			maps.Copy(got, batch)
			return nil
		})
		require.NoError(t, err)
		require.Len(t, got, 2)
		require.Contains(t, got, "1")
		require.Contains(t, got, "3")
	})
	t.Run("exec scan", func(t *testing.T) {
		st := newFakeStore(seed(), &fakeAdmin{}, "books", typeMap{}, ctAuto)
		got, err := st.Query(context.Background(), []string{"scan", "books"})
		require.NoError(t, err)
		require.Len(t, got, 2)
		require.Contains(t, got, "1")
		require.Contains(t, got, "3")
	})
	t.Run("clear", func(t *testing.T) {
		fc := seed()
		st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
		require.NoError(t, st.Clear(context.Background()))
		require.Equal(t, 2, fc.delCalls)
		require.NotContains(t, fc.rows, "1")
		require.NotContains(t, fc.rows, "3")
	})
}

func TestParseTypeMapReturnsAnAllocatedMapForNoEntries(t *testing.T) {
	m, err := parseTypeMap("")
	require.NoError(t, err)
	require.NotNil(t, m)
	require.Equal(t, typeMap{}, m)
}

package hbase

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tsuna/gohbase/filter"
	"github.com/tsuna/gohbase/pb"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// seedBooks fills a fake with two rows, the shared fixture for the request-shape
// assertions below.
func seedBooks(f *fakeClient) {
	f.seed("1", cell("cf", "title", "Dune"))
	f.seed("2", cell("cf", "title", "Hyperion"))
}

// wantSingleColumnFilter is the filter eqToFilter builds for cf:author = "Herbert":
// a binary-comparator equality that also excludes rows lacking the column and reads
// only the latest version.
func wantSingleColumnFilter(value string) filterConstructor {
	return filter.NewSingleColumnValueFilter(
		[]byte("cf"), []byte("author"), filter.Equal,
		filter.NewBinaryComparator(filter.NewByteArrayComparable([]byte(value))),
		true, true,
	)
}

func TestGetIssuesBoundedSingleVersionGet(t *testing.T) {
	fc := newFakeClient()
	seedBooks(fc)
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	_, err := st.Get(testCtx(), []string{"1", "2"})
	require.NoError(t, err)
	require.Len(t, fc.gets, 2)
	for _, g := range fc.gets {
		requireTestCtx(t, g.ctx)
		require.Equal(t, "books", g.table)
		requireDefaultMaxVersions(t, getPB(t, g).MaxVersions)
		require.Nil(t, getPB(t, g).ExistenceOnly, "a value read must not be existence-only")
	}
}

func TestGetEmptyKeysIssuesNoRequest(t *testing.T) {
	fc := newFakeClient()
	seedBooks(fc)
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	got, err := st.Get(testCtx(), nil)
	require.NoError(t, err)
	// An empty key list short-circuits: an addressable empty map, no round trip.
	require.NotNil(t, got)
	require.Equal(t, map[string]any{}, got)
	require.Empty(t, fc.gets)
}

func TestScanBatchesIssuesSingleVersionUnfilteredScan(t *testing.T) {
	fc := newFakeClient()
	seedBooks(fc)
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	require.NoError(t, st.ScanBatches(testCtx(), func(map[string]any) error { return nil }))
	require.Len(t, fc.scans, 1)
	requireTestCtx(t, fc.scans[0].ctx)
	require.Equal(t, "books", fc.scans[0].table)
	requireDefaultMaxVersions(t, scanPB(t, fc.scans[0]).MaxVersions)
	require.Nil(t, scanPB(t, fc.scans[0]).Filter, "an unfiltered scan pushes no filter")
}

func TestTypedScanIssuesTheSameScan(t *testing.T) {
	fc := newFakeClient()
	seedBooks(fc)
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	require.NoError(t, st.TypedScan(testCtx(), func([]query.Record) error { return nil }))
	require.Len(t, fc.scans, 1)
	requireTestCtx(t, fc.scans[0].ctx)
}

func TestScanFilteredPushesTheCompiledFilter(t *testing.T) {
	t.Run("pushable equality", func(t *testing.T) {
		fc := newFakeClient()
		seedBooks(fc)
		st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

		pred := predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"}
		require.NoError(t, st.ScanFiltered(testCtx(), pred, func(map[string]any) error { return nil }))
		require.Len(t, fc.scans, 1)
		requireTestCtx(t, fc.scans[0].ctx)
		require.Equal(t, "books", fc.scans[0].table)
		requireDefaultMaxVersions(t, scanPB(t, fc.scans[0]).MaxVersions)
		requireFilter(t, scanPB(t, fc.scans[0]).Filter, wantSingleColumnFilter("Herbert"))
	})

	t.Run("nothing pushable scans unfiltered", func(t *testing.T) {
		fc := newFakeClient()
		seedBooks(fc)
		st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

		pred := predicate.Cmp{Path: []string{"cf", "year"}, Op: predicate.Gt, Value: float64(1)}
		require.NoError(t, st.ScanFiltered(testCtx(), pred, func(map[string]any) error { return nil }))
		require.Len(t, fc.scans, 1)
		require.Nil(t, scanPB(t, fc.scans[0]).Filter)
		requireDefaultMaxVersions(t, scanPB(t, fc.scans[0]).MaxVersions)
	})
}

func TestExecGetIssuesTheNamedTablesGet(t *testing.T) {
	fc := newFakeClient()
	fc.seed("42", cell("cf", "title", "Dune"))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	_, err := st.Query(testCtx(), []string{"get", "other", "42"})
	require.NoError(t, err)
	require.Len(t, fc.gets, 1)
	requireTestCtx(t, fc.gets[0].ctx)
	// The verb names its own table; the row key is the second argument, not the table.
	require.Equal(t, "other", fc.gets[0].table)
	require.Equal(t, []byte("42"), getPB(t, fc.gets[0]).Row)
	requireDefaultMaxVersions(t, getPB(t, fc.gets[0]).MaxVersions)
}

func TestExecScanIssuesTheNamedTablesScan(t *testing.T) {
	fc := newFakeClient()
	seedBooks(fc)
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	_, err := st.Query(testCtx(), []string{"scan", "other"})
	require.NoError(t, err)
	require.Len(t, fc.scans, 1)
	requireTestCtx(t, fc.scans[0].ctx)
	require.Equal(t, "other", fc.scans[0].table)
	requireDefaultMaxVersions(t, scanPB(t, fc.scans[0]).MaxVersions)
	require.Nil(t, scanPB(t, fc.scans[0]).Filter)
}

func TestExecCountScansKeyOnly(t *testing.T) {
	fc := newFakeClient()
	seedBooks(fc)
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	got, err := st.Query(testCtx(), []string{"count", "other"})
	require.NoError(t, err)
	require.Equal(t, 2, got)
	require.Len(t, fc.scans, 1)
	requireTestCtx(t, fc.scans[0].ctx)
	require.Equal(t, "other", fc.scans[0].table)
	// Key-only, and not the length-as-value form: a count never ships cell values.
	requireFilter(t, scanPB(t, fc.scans[0]).Filter, filter.NewKeyOnlyFilter(false))
}

func TestExecPutIssuesTheNamedTablesPut(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	got, err := st.Query(testCtx(), []string{"put", "other", "7", "cf:title", "Sabriel"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"ok": true}, got)
	require.Len(t, fc.puts, 1)
	requireTestCtx(t, fc.puts[0].ctx)
	require.Equal(t, "other", fc.puts[0].table)
}

func TestExecDeleteIssuesTheNamedTablesDelete(t *testing.T) {
	fc := newFakeClient()
	fc.seed("7", cell("cf", "title", "Sabriel"))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	got, err := st.Query(testCtx(), []string{"delete", "other", "7"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"ok": true}, got)
	require.Len(t, fc.dels, 1)
	requireTestCtx(t, fc.dels[0].ctx)
	require.Equal(t, "other", fc.dels[0].table)
}

func TestPutIssuesAnExistenceOnlyPreReadThenAPut(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	_, err := st.Put(testCtx(),
		[]query.Record{{Key: "1", Value: map[string]any{"cf": map[string]any{"title": "Dune"}}}},
		query.Upsert)
	require.NoError(t, err)
	require.Len(t, fc.gets, 1)
	requireTestCtx(t, fc.gets[0].ctx)
	require.Equal(t, "books", fc.gets[0].table)
	// The pre-read is accounting only, so it asks the server for existence, not cells.
	require.True(t, getPB(t, fc.gets[0]).GetExistenceOnly())
	require.Len(t, fc.puts, 1)
	requireTestCtx(t, fc.puts[0].ctx)
	require.Equal(t, "books", fc.puts[0].table)
}

func TestPutInsertOnlyIssuesACheckAndPutWithNoPreRead(t *testing.T) {
	fc := newFakeClient()
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	_, err := st.Put(testCtx(),
		[]query.Record{{Key: "1", Value: map[string]any{"cf": map[string]any{"title": "Dune"}}}},
		query.InsertOnly)
	require.NoError(t, err)
	// CheckAndPut is atomic, so insert-only needs no existence pre-read.
	require.Empty(t, fc.gets)
	require.Len(t, fc.cas, 1)
	requireTestCtx(t, fc.cas[0].ctx)
	require.Equal(t, "books", fc.cas[0].table)
}

func TestDeleteIssuesAnExistenceOnlyPreReadThenADelete(t *testing.T) {
	fc := newFakeClient()
	fc.seed("1", cell("cf", "title", "Dune"))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	stat, err := st.Delete(testCtx(), []string{"1"})
	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Deleted: 1}, stat)
	require.Len(t, fc.gets, 1)
	requireTestCtx(t, fc.gets[0].ctx)
	require.True(t, getPB(t, fc.gets[0]).GetExistenceOnly())
	require.Len(t, fc.dels, 1)
	requireTestCtx(t, fc.dels[0].ctx)
	require.Equal(t, "books", fc.dels[0].table)
}

func TestClearScansKeyOnlyThenDeletesEachRow(t *testing.T) {
	fc := newFakeClient()
	seedBooks(fc)
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)

	require.NoError(t, st.Clear(testCtx()))
	require.Len(t, fc.scans, 1)
	requireTestCtx(t, fc.scans[0].ctx)
	require.Equal(t, "books", fc.scans[0].table)
	// Key-only, and not the length-as-value form: clearing never ships cell values.
	requireFilter(t, scanPB(t, fc.scans[0]).Filter, filter.NewKeyOnlyFilter(false))
	require.Len(t, fc.dels, 2)
	for _, d := range fc.dels {
		requireTestCtx(t, d.ctx)
		require.Equal(t, "books", d.table)
	}
}

func TestDropBindsTheContextToBothAdminCalls(t *testing.T) {
	fa := &fakeAdmin{}
	st := newFakeStore(newFakeClient(), fa, "books", typeMap{}, ctAuto)

	require.NoError(t, st.Drop(testCtx()))
	require.Len(t, fa.disableCalls, 1)
	requireTestCtx(t, fa.disableCalls[0].ctx)
	require.Len(t, fa.deleteCalls, 1)
	requireTestCtx(t, fa.deleteCalls[0].ctx)
}

func TestTableListingsBindTheContextAndNamespace(t *testing.T) {
	tests := []struct {
		name  string
		table string
		want  string
	}{
		{"unqualified table lists the default namespace", "books", defaultNamespace},
		{"qualified table lists its own namespace", "app:events", "app"},
		{"no table lists the default namespace", "", defaultNamespace},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fa := &fakeAdmin{tables: []*pb.TableName{tableName(tt.want, "books")}}
			st := newFakeStore(newFakeClient(), fa, tt.table, typeMap{}, ctAuto)
			_, err := st.InspectTables(testCtx())
			require.NoError(t, err)
			require.Len(t, fa.listCalls, 1)
			requireTestCtx(t, fa.listCalls[0].ctx)
			require.Equal(t, tt.want, listedNamespace(t, fa.listCalls[0]))
		})
	}
}

func TestVerifyTableListsTheTablesOwnNamespace(t *testing.T) {
	fa := &fakeAdmin{tables: []*pb.TableName{tableName("app", "events")}}
	st := newFakeStore(newFakeClient(), fa, "", typeMap{}, ctAuto)

	require.NoError(t, st.verifyTable(testCtx(), "app:events"))
	require.Len(t, fa.listCalls, 1)
	requireTestCtx(t, fa.listCalls[0].ctx)
	require.Equal(t, "app", listedNamespace(t, fa.listCalls[0]))
}

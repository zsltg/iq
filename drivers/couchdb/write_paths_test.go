package couchdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/go-kivik/kivik/v4/driver"
	"github.com/go-kivik/kivik/v4/mockdb"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// bulkRecorder answers bulk writes and records the documents of each one.
type bulkRecorder struct {
	t       *testing.T
	calls   [][]any
	results [][]driver.BulkResult
	failAt  int
}

// answer is the WillExecute callback. It copies the documents, then returns the
// scripted results for this call, or an error at call failAt (1-based).
func (b *bulkRecorder) answer(ctx context.Context, docs []any, _ driver.Options) ([]driver.BulkResult, error) {
	requireKeyed(b.t, ctx)
	b.calls = append(b.calls, append([]any(nil), docs...))
	n := len(b.calls)
	if n == b.failAt {
		return nil, errors.New("service unavailable")
	}
	if n <= len(b.results) {
		return b.results[n-1], nil
	}
	res := make([]driver.BulkResult, len(docs))
	for i, d := range docs {
		id, _ := d.(map[string]any)["_id"].(string)
		res[i] = driver.BulkResult{ID: id, Rev: "2-x"}
	}
	return res, nil
}

// expectRevs answers the revision read (one _all_docs request) and checks the context.
func expectRevs(t *testing.T, mdb *mockdb.DB, rows *mockdb.Rows) {
	t.Helper()
	expectAllDocsWithCtx(t, mdb, rows)
}

func TestUpsertSendsIdentityAndCountsOutcomes(t *testing.T) {
	// A key adds _id. A key with a prior revision adds _rev. A keyless record gets
	// neither. Identity fields inside the value are replaced. The stat counts the
	// record with a prior revision as overwritten and the rest as written.
	st, _, mdb := newMockStore(t, 10, 2)
	expectRevs(t, mdb, mockdb.NewRows().AddRow(revRow("a", "1-a")))
	rec := &bulkRecorder{t: t}
	mdb.ExpectBulkDocs().WillExecute(rec.answer)

	stat, err := st.Put(keyedCtx(), []query.Record{
		{Key: "a", Value: map[string]any{"title": "one", "_id": "zz", "_rev": "9-z"}},
		{Key: "b", Value: map[string]any{"x": 1}},
		{Value: map[string]any{"y": 2}},
	}, query.Upsert)

	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Overwritten: 1, Written: 2}, stat)
	require.Equal(t, [][]any{{
		map[string]any{"title": "one", "_id": "a", "_rev": "1-a"},
		map[string]any{"x": 1, "_id": "b"},
		map[string]any{"y": 2},
	}}, rec.calls)
}

func TestInsertOnlySendsNoRevision(t *testing.T) {
	// An insert sets _id from the key and never sets _rev, even when the value holds one.
	st, _, mdb := newMockStore(t, 10, 1)
	rec := &bulkRecorder{t: t}
	mdb.ExpectBulkDocs().WillExecute(rec.answer)

	stat, err := st.Put(keyedCtx(), []query.Record{
		{Key: "a", Value: map[string]any{"t": 1, "_rev": "9-z"}},
		{Value: map[string]any{"y": 2}},
	}, query.InsertOnly)

	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 2}, stat)
	require.Equal(t, [][]any{{
		map[string]any{"t": 1, "_id": "a"},
		map[string]any{"y": 2},
	}}, rec.calls)
}

func TestInsertOnlyCountsConflictsAsSkips(t *testing.T) {
	conflict := statusError{code: http.StatusConflict}
	tests := []struct {
		name    string
		results []driver.BulkResult
		want    query.WriteStat
		wantErr string
	}{
		{
			name:    "conflicts are skipped",
			results: []driver.BulkResult{{ID: "1", Error: conflict}, {ID: "2"}, {ID: "3", Error: conflict}},
			want:    query.WriteStat{Written: 1, Skipped: 2},
		},
		{
			name:    "another failure after a conflict fails the batch",
			results: []driver.BulkResult{{ID: "1", Error: conflict}, {ID: "2", Error: statusError{code: http.StatusInternalServerError}}},
			wantErr: "couchdb insert",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, mdb := newMockStore(t, 10, 1)
			rec := &bulkRecorder{t: t, results: [][]driver.BulkResult{tt.results}}
			mdb.ExpectBulkDocs().WillExecute(rec.answer)
			batch := []query.Record{
				{Key: "1", Value: map[string]any{}},
				{Key: "2", Value: map[string]any{}},
				{Key: "3", Value: map[string]any{}},
			}[:len(tt.results)]

			stat, err := st.Put(keyedCtx(), batch, query.InsertOnly)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Equal(t, query.WriteStat{}, stat)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, stat)
		})
	}
}

func TestInsertOnlyRejectsALaterNonObjectBeforeAnyRequest(t *testing.T) {
	st, mock, _ := newMockStore(t, 10, 0)

	stat, err := st.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{}},
		{Key: "2", Value: "scalar"},
	}, query.InsertOnly)

	require.ErrorContains(t, err, `value for key "2" is not a JSON object`)
	require.Equal(t, query.WriteStat{}, stat)
	require.NoError(t, mock.ExpectationsWereMet())
}

// liveRevs builds _all_docs rows with a revision for each key.
func liveRevs(keys ...string) *mockdb.Rows {
	rows := mockdb.NewRows()
	for _, k := range keys {
		rows.AddRow(revRow(k, "1-"+k))
	}
	return rows
}

func TestDeleteChunksTheTombstones(t *testing.T) {
	// Tombstones go out in chunks of pageSize, in key order, each with its revision.
	st, _, mdb := newMockStore(t, 2, 2)
	expectRevs(t, mdb, liveRevs("k1", "k2", "k3", "k4", "k5"))
	rec := &bulkRecorder{t: t}
	mdb.ExpectBulkDocs().WillExecute(rec.answer)
	mdb.ExpectBulkDocs().WillExecute(rec.answer)
	mdb.ExpectBulkDocs().WillExecute(rec.answer)

	stat, err := st.Delete(keyedCtx(), []string{"k1", "k2", "k3", "k4", "k5"})

	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Deleted: 5}, stat)
	tomb := func(k string) any {
		return map[string]any{"_id": k, "_rev": "1-" + k, "_deleted": true}
	}
	require.Equal(t, [][]any{
		{tomb("k1"), tomb("k2")},
		{tomb("k3"), tomb("k4")},
		{tomb("k5")},
	}, rec.calls)
}

func TestDeleteReturnsAZeroStatWhenALaterChunkFails(t *testing.T) {
	st, _, mdb := newMockStore(t, 2, 2)
	expectRevs(t, mdb, liveRevs("k1", "k2", "k3"))
	rec := &bulkRecorder{t: t, failAt: 2}
	mdb.ExpectBulkDocs().WillExecute(rec.answer)
	mdb.ExpectBulkDocs().WillExecute(rec.answer)

	stat, err := st.Delete(keyedCtx(), []string{"k1", "k2", "k3"})

	require.ErrorContains(t, err, "couchdb bulk delete")
	require.Equal(t, query.DeleteStat{}, stat)
	require.Len(t, rec.calls, 2)
}

func TestDeleteOfOnlyMissingKeysSendsNoBulkWrite(t *testing.T) {
	// Both handle resolutions happen (the mock expects two), but no bulk request goes out.
	st, mock, mdb := newMockStore(t, 2, 2)
	expectRevs(t, mdb, mockdb.NewRows())

	stat, err := st.Delete(keyedCtx(), []string{"a", "b", "c"})

	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Missing: 3}, stat)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestScanDeletesBatchBoundariesAndContext(t *testing.T) {
	tests := []struct {
		n    int
		want []int
	}{
		{5, []int{2, 2, 1}},
		{4, []int{2, 2}},
		{0, nil},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d rows", tt.n), func(t *testing.T) {
			st, _, mdb := newMockStore(t, 2, 1)
			keys := make([]string, tt.n)
			for i := range keys {
				keys[i] = fmt.Sprintf("k%d", i)
			}
			expectRevs(t, mdb, liveRevs(keys...))

			var sizes []int
			require.NoError(t, st.scanDeletes(keyedCtx(), func(batch []any) error {
				require.NotEmpty(t, batch)
				sizes = append(sizes, len(batch))
				return nil
			}))

			require.Equal(t, tt.want, sizes)
		})
	}
}

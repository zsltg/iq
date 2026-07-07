package elasticsearch

import (
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// books are the sample documents most integration tests seed.
func books() []map[string]any {
	return []map[string]any{
		{"_id": "1", "title": "Dune", "author": "Frank Herbert", "year": 1965, "active": true},
		{"_id": "2", "title": "Neuromancer", "author": "William Gibson", "year": 1984, "active": true},
		{"_id": "3", "title": "Chapterhouse", "author": "Frank Herbert", "year": 1985, "active": false},
	}
}

// collect drains a scan into one map keyed by _id.
func collect(t *testing.T, scan func(func(map[string]any) error) error) map[string]any {
	t.Helper()
	out := map[string]any{}
	require.NoError(t, scan(func(batch map[string]any) error {
		for k, v := range batch {
			out[k] = v
		}
		return nil
	}))
	return out
}

func TestGet(t *testing.T) {
	st := seedIndex(t, books()...)
	ctx := skipShort(t)

	got, err := st.Get(ctx, []string{"1", "3", "missing"})
	require.NoError(t, err)
	require.Len(t, got, 3)

	doc := got["1"].(map[string]any)
	require.Equal(t, "Dune", doc["title"])
	require.Equal(t, 1965, doc["year"])
	require.Equal(t, "1", doc["_id"]) // the _id is injected into the value.
	require.Nil(t, got["missing"])
}

func TestGetEmptyKeys(t *testing.T) {
	st := seedIndex(t, books()...)
	ctx := skipShort(t)
	got, err := st.Get(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestScanBatches(t *testing.T) {
	st := seedIndex(t, books()...)
	ctx := skipShort(t)

	all := collect(t, func(fn func(map[string]any) error) error {
		return st.ScanBatches(ctx, fn)
	})
	require.Len(t, all, 3)
	ids := make([]string, 0, len(all))
	for k := range all {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	require.Equal(t, []string{"1", "2", "3"}, ids)
}

func TestScanBatchesPaging(t *testing.T) {
	// Seed more than one page so the point-in-time + search_after loop iterates.
	docs := make([]map[string]any, 0, 250)
	for i := 0; i < 250; i++ {
		id := strconv.Itoa(i)
		docs = append(docs, map[string]any{"_id": id, "n": i})
	}
	st := seedIndex(t, docs...)
	ctx := skipShort(t)

	all := collect(t, func(fn func(map[string]any) error) error {
		return st.ScanBatches(ctx, fn)
	})
	require.Len(t, all, 250)
}

func TestScanFilteredPushesEquality(t *testing.T) {
	st := seedIndex(t, books()...)
	ctx := skipShort(t)

	// author == "Frank Herbert" is pushable (text field's keyword sub-field).
	pred := predicate.Eq{Path: []string{"author"}, Value: "Frank Herbert"}
	got := collect(t, func(fn func(map[string]any) error) error {
		return st.ScanFiltered(ctx, pred, fn)
	})
	ids := keysOf(got)
	require.Equal(t, []string{"1", "3"}, ids)
}

func TestScanFilteredFallsBackForRange(t *testing.T) {
	st := seedIndex(t, books()...)
	ctx := skipShort(t)

	// A range does not push; ScanFiltered must fall back to a full walk (the caller
	// re-runs the jq), so every document is still returned.
	pred := predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 1970.0}
	got := collect(t, func(fn func(map[string]any) error) error {
		return st.ScanFiltered(ctx, pred, fn)
	})
	require.Len(t, got, 3)
}

func TestEstimateCount(t *testing.T) {
	st := seedIndex(t, books()...)
	ctx := skipShort(t)
	n, err := st.EstimateCount(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(3), n)
}

func TestQueryRaw(t *testing.T) {
	st := seedIndex(t, books()...)
	ctx := skipShort(t)

	// A bare query object is wrapped as {"query": ...}.
	reply, err := st.Query(ctx, []string{`{"term":{"author.keyword":"William Gibson"}}`})
	require.NoError(t, err)
	m := reply.(map[string]any)
	hits := m["hits"].(map[string]any)
	total := hits["total"].(map[string]any)
	require.Equal(t, 1, total["value"])
}

func TestPutUpsertAndInsertOnly(t *testing.T) {
	st := seedIndex(t)
	ctx := skipShort(t)

	// First write: two new documents.
	stat, err := st.Put(ctx, []query.Record{
		{Key: "1", Value: map[string]any{"title": "Dune"}},
		{Key: "2", Value: map[string]any{"title": "Neuromancer"}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 2, stat.Written)
	require.Equal(t, 0, stat.Overwritten)

	// Upsert an existing id: it is overwritten, not written anew.
	stat, err = st.Put(ctx, []query.Record{
		{Key: "1", Value: map[string]any{"title": "Dune Messiah"}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 0, stat.Written)
	require.Equal(t, 1, stat.Overwritten)

	// Insert-only over an existing id skips it.
	stat, err = st.Put(ctx, []query.Record{
		{Key: "1", Value: map[string]any{"title": "ignored"}},
		{Key: "3", Value: map[string]any{"title": "Chapterhouse"}},
	}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Written)
	require.Equal(t, 1, stat.Skipped)

	got, err := st.Get(ctx, []string{"1"})
	require.NoError(t, err)
	require.Equal(t, "Dune Messiah", got["1"].(map[string]any)["title"])
}

func TestClear(t *testing.T) {
	st := seedIndex(t, books()...)
	ctx := skipShort(t)

	require.NoError(t, st.Clear(ctx))
	n, err := st.EstimateCount(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), n) // documents gone, index kept.
}

func TestDrop(t *testing.T) {
	st := seedIndex(t, books()...)
	ctx := skipShort(t)

	require.NoError(t, st.Drop(ctx))
	_, err := st.EstimateCount(ctx)
	require.Error(t, err) // the index no longer exists.
}

func TestTypedScan(t *testing.T) {
	st := seedIndex(t, books()...)
	ctx := skipShort(t)

	seen := map[string]query.Record{}
	require.NoError(t, st.TypedScan(ctx, func(batch []query.Record) error {
		for _, r := range batch {
			seen[r.Key] = r
		}
		return nil
	}))
	require.Len(t, seen, 3)
	require.Equal(t, "document", seen["1"].Type)
}

func TestInspect(t *testing.T) {
	st := seedIndex(t, books()...)
	ctx := skipShort(t)

	server, err := st.InspectServer(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, server.(map[string]any)["version"])

	indices, err := st.InspectIndices(ctx)
	require.NoError(t, err)
	require.NotNil(t, indices.(map[string]any)["indices"])

	mapping, err := st.InspectMapping(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, mapping)

	aliases, err := st.InspectAliases(ctx)
	require.NoError(t, err)
	require.NotNil(t, aliases.(map[string]any)["aliases"])
}

func TestErrorMessageSurfacesTypeAndReason(t *testing.T) {
	ctx := skipShort(t)
	// A store on an index that does not exist opens fine (the mapping read is
	// best-effort), but a query surfaces the Elasticsearch error type and reason —
	// a clean message, never a raw body.
	st, err := Open(ctx, testURL(), "iq_test_missing_index_zzz", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	_, err = st.Query(ctx, []string{`{"query":{"match_all":{}}}`})
	require.Error(t, err)
	require.ErrorContains(t, err, "index_not_found_exception")
	require.ErrorContains(t, err, "no such index")
}

func TestConnectVerifiesReachability(t *testing.T) {
	ctx := skipShort(t)
	// A bad port fails fast at open (the info probe), not at first query.
	_, err := Open(ctx, "elasticsearch://localhost:1/", "books", nil, 0)
	require.Error(t, err)
}

// keysOf returns a scan result's sorted keys.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

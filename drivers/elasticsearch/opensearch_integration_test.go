package elasticsearch

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// requireOpenSearch skips a test when no OpenSearch backend is available, returning
// its base URL otherwise. These tests exercise the opensearch-go client, the
// OpenSearch point-in-time endpoints, and the keyset sort — the paths that differ from
// Elasticsearch; the shared logic is covered by the Elasticsearch suite.
func requireOpenSearch(t *testing.T) string {
	t.Helper()
	u := osURL()
	if u == "" {
		t.Skip("skipping opensearch integration test: no OpenSearch backend (set IQ_OPENSEARCH_URL)")
	}
	return u
}

func TestOpenSearchGet(t *testing.T) {
	st := seedIndexOn(t, requireOpenSearch(t), books()...)
	ctx := skipShort(t)

	got, err := st.Get(ctx, []string{"1", "3", "missing"})
	require.NoError(t, err)
	require.Len(t, got, 2, "only the two existing keys come back")
	doc := got["1"].(map[string]any)
	require.Equal(t, "Dune", doc["title"])
	require.Equal(t, 1965, doc["year"])
	require.Equal(t, "1", doc["_id"])
	require.NotContains(t, got, "missing", "a key with no document is absent from the map")
}

func TestOpenSearchScanPIT(t *testing.T) {
	st := seedIndexOn(t, requireOpenSearch(t), books()...)
	ctx := skipShort(t)

	all := collect(t, func(fn func(map[string]any) error) error {
		return st.ScanBatches(ctx, fn)
	})
	require.Equal(t, []string{"1", "2", "3"}, keysOf(all))
}

func TestOpenSearchScanPITPaging(t *testing.T) {
	// More than one page so the OpenSearch point-in-time + search_after loop iterates.
	docs := make([]map[string]any, 0, 250)
	for i := range 250 {
		docs = append(docs, map[string]any{"_id": strconv.Itoa(i), "n": i})
	}
	st := seedIndexOn(t, requireOpenSearch(t), docs...)
	ctx := skipShort(t)

	all := collect(t, func(fn func(map[string]any) error) error {
		return st.ScanBatches(ctx, fn)
	})
	require.Len(t, all, 250)
}

func TestOpenSearchScanFilteredPushdown(t *testing.T) {
	st := seedIndexOn(t, requireOpenSearch(t), books()...)
	ctx := skipShort(t)

	pred := predicate.Eq{Path: []string{"author"}, Value: "Frank Herbert"}
	got := collect(t, func(fn func(map[string]any) error) error {
		return st.ScanFiltered(ctx, pred, fn)
	})
	require.Equal(t, []string{"1", "3"}, keysOf(got))
}

func TestOpenSearchEstimateCount(t *testing.T) {
	st := seedIndexOn(t, requireOpenSearch(t), books()...)
	ctx := skipShort(t)
	n, err := st.EstimateCount(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(3), n)
}

func TestOpenSearchWriteClearDrop(t *testing.T) {
	base := requireOpenSearch(t)
	st := seedIndexOn(t, base)
	ctx := skipShort(t)

	stat, err := st.Put(ctx, []query.Record{
		{Key: "1", Value: map[string]any{"title": "Dune"}},
		{Key: "2", Value: map[string]any{"title": "Neuromancer"}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 2, stat.Written)

	stat, err = st.Put(ctx, []query.Record{{Key: "1", Value: map[string]any{"title": "Dune Messiah"}}}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Overwritten)

	stat, err = st.Put(ctx, []query.Record{{Key: "1", Value: map[string]any{"title": "x"}}}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Skipped)

	require.NoError(t, st.Clear(ctx))
	n, err := st.EstimateCount(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), n)

	require.NoError(t, st.Drop(ctx))
	_, err = st.EstimateCount(ctx)
	require.Error(t, err)
}

func TestOpenSearchDelete(t *testing.T) {
	st := seedIndexOn(
		t, requireOpenSearch(t),
		map[string]any{"_id": "d1", "title": "Dune"},
		map[string]any{"_id": "d2", "title": "Neuromancer"},
		map[string]any{"_id": "keep", "title": "Chapterhouse"},
	)
	ctx := skipShort(t)

	stat, err := st.Delete(ctx, []string{"d1", "d2", "absent"})
	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{Deleted: 2, Missing: 1}, stat)

	got, err := st.Get(ctx, []string{"d1", "d2", "keep"})
	require.NoError(t, err)
	require.Nil(t, got["d1"])
	require.Nil(t, got["d2"])
	require.Equal(t, "Chapterhouse", got["keep"].(map[string]any)["title"])
}

func TestOpenSearchQueryRaw(t *testing.T) {
	st := seedIndexOn(t, requireOpenSearch(t), books()...)
	ctx := skipShort(t)

	reply, err := st.Query(ctx, []string{`{"term":{"author.keyword":"William Gibson"}}`})
	require.NoError(t, err)
	hits := reply.(map[string]any)["hits"].(map[string]any)
	total := hits["total"].(map[string]any)
	require.Equal(t, 1, total["value"])
}

func TestOpenSearchInspect(t *testing.T) {
	st := seedIndexOn(t, requireOpenSearch(t), books()...)
	ctx := skipShort(t)

	server, err := st.InspectServer(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, server.(map[string]any)["version"])

	mapping, err := st.InspectMapping(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, mapping)
}

func TestOpenSearchErrorMessageSurfacesTypeAndReason(t *testing.T) {
	base := requireOpenSearch(t)
	ctx := skipShort(t)

	st, err := Open(ctx, base, "iq_test_missing_index_zzz", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	_, err = st.Query(ctx, []string{`{"query":{"match_all":{}}}`})
	require.Error(t, err)
	require.ErrorContains(t, err, "opensearch search:")
	require.ErrorContains(t, err, "index_not_found_exception")
}

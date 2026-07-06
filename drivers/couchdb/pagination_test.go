package couchdb

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// kindDocs builds n documents with sortable ids "d00".."d<n>" all sharing a field,
// so a selector matches every one — used to drive multi-page scans deterministically.
func kindDocs(n int) []map[string]any {
	docs := make([]map[string]any, n)
	for i := range docs {
		docs[i] = map[string]any{"_id": fmt.Sprintf("d%02d", i), "kind": "book", "seq": i}
	}
	return docs
}

// scanPages records the size of each page a scan emits and the union of keys,
// asserting no page is ever empty (which pins the final len(page) > 0 guard).
func scanPages(t *testing.T, scan func(func(map[string]any) error) error) (sizes []int, keys []string) {
	t.Helper()
	seen := map[string]any{}
	require.NoError(t, scan(func(batch map[string]any) error {
		require.NotEmpty(t, batch, "a page handed to the caller is never empty")
		sizes = append(sizes, len(batch))
		for k, v := range batch {
			seen[k] = v
		}
		return nil
	}))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return sizes, keys
}

func TestScanBatchesPageStructure(t *testing.T) {
	st := seedDB(t, kindDocs(5)...)
	st.pageSize = 2
	ctx := skipShort(t)

	sizes, keys := scanPages(t, func(fn func(map[string]any) error) error { return st.ScanBatches(ctx, fn) })
	require.Equal(t, []int{2, 2, 1}, sizes, "pages fill to pageSize, last is the remainder")
	require.Len(t, keys, 5)
}

func TestScanBatchesExactMultiple(t *testing.T) {
	st := seedDB(t, kindDocs(4)...)
	st.pageSize = 2
	ctx := skipShort(t)

	// Four docs at pageSize 2 is exactly two full pages; the trailing partial page
	// must not be emitted (no empty final page).
	sizes, keys := scanPages(t, func(fn func(map[string]any) error) error { return st.ScanBatches(ctx, fn) })
	require.Equal(t, []int{2, 2}, sizes)
	require.Len(t, keys, 4)
}

func TestScanBatchesFnErrorPropagates(t *testing.T) {
	st := seedDB(t, kindDocs(4)...)
	st.pageSize = 2
	ctx := skipShort(t)

	sentinel := errors.New("stop")
	err := st.ScanBatches(ctx, func(map[string]any) error { return sentinel })
	require.ErrorIs(t, err, sentinel)
}

func TestScanFilteredMultiPage(t *testing.T) {
	st := seedDB(t, kindDocs(5)...)
	st.pageSize = 2
	ctx := skipShort(t)

	// Every doc matches, so _find must follow its bookmark across three pages.
	sizes, keys := scanPages(t, func(fn func(map[string]any) error) error {
		return st.ScanFiltered(ctx, predicate.Eq{Path: []string{"kind"}, Value: "book"}, fn)
	})
	require.Equal(t, []int{2, 2, 1}, sizes)
	require.Len(t, keys, 5)
}

func TestScanFilteredExactMultiple(t *testing.T) {
	st := seedDB(t, kindDocs(4)...)
	st.pageSize = 2
	ctx := skipShort(t)

	// Four matches at limit 2: the third _find returns no rows, and that empty page
	// must not reach the caller.
	sizes, keys := scanPages(t, func(fn func(map[string]any) error) error {
		return st.ScanFiltered(ctx, predicate.Eq{Path: []string{"kind"}, Value: "book"}, fn)
	})
	require.Equal(t, []int{2, 2}, sizes)
	require.Len(t, keys, 4)
}

func TestScanFilteredFnErrorPropagates(t *testing.T) {
	st := seedDB(t, kindDocs(4)...)
	st.pageSize = 2
	ctx := skipShort(t)

	sentinel := errors.New("stop")
	err := st.ScanFiltered(ctx, predicate.Eq{Path: []string{"kind"}, Value: "book"}, func(map[string]any) error { return sentinel })
	require.ErrorIs(t, err, sentinel)
}

func TestClearPaginates(t *testing.T) {
	st := seedDB(t, kindDocs(5)...)
	st.pageSize = 2
	ctx := skipShort(t)

	require.NoError(t, st.Clear(ctx))

	n, err := st.EstimateCount(ctx)
	require.NoError(t, err)
	require.Zero(t, n, "every document across all pages is deleted")
}

// scanDeleteBatches records the size of each delete batch scanDeletes yields, and
// the total, asserting no batch is ever empty (pinning the trailing len > 0 guard).
func scanDeleteBatches(t *testing.T, st *Store, ctx context.Context) (sizes []int, total int) {
	t.Helper()
	require.NoError(t, st.scanDeletes(ctx, func(batch []any) error {
		require.NotEmpty(t, batch, "a delete batch is never empty")
		sizes = append(sizes, len(batch))
		total += len(batch)
		return nil
	}))
	return sizes, total
}

func TestScanDeletesBatchStructure(t *testing.T) {
	st := seedDB(t, kindDocs(5)...)
	st.pageSize = 2
	ctx := skipShort(t)

	// Five tombstones at pageSize 2: batches fill to pageSize, the last is the
	// remainder — the boundary that pins the flush threshold.
	sizes, total := scanDeleteBatches(t, st, ctx)
	require.Equal(t, []int{2, 2, 1}, sizes)
	require.Equal(t, 5, total)
}

func TestScanDeletesExactMultiple(t *testing.T) {
	st := seedDB(t, kindDocs(4)...)
	st.pageSize = 2
	ctx := skipShort(t)

	// Four tombstones at pageSize 2 is exactly two full batches; no empty trailing
	// batch is yielded.
	sizes, total := scanDeleteBatches(t, st, ctx)
	require.Equal(t, []int{2, 2}, sizes)
	require.Equal(t, 4, total)
}

func TestScanDeletesFnErrorPropagates(t *testing.T) {
	st := seedDB(t, kindDocs(4)...)
	st.pageSize = 2
	ctx := skipShort(t)

	sentinel := errors.New("stop")
	err := st.scanDeletes(ctx, func([]any) error { return sentinel })
	require.ErrorIs(t, err, sentinel)
}

func TestQueryReturnsBookmark(t *testing.T) {
	st := seedDB(t, booksDocs()...)
	ctx := skipShort(t)

	res, err := st.Query(ctx, []string{`{"selector": {"year": {"$gt": 2000}}}`})
	require.NoError(t, err)
	// CouchDB _find always returns a paging bookmark; the driver surfaces it.
	require.NotEmpty(t, res.(map[string]any)["bookmark"])
}

func TestInspectServerReportsFeatures(t *testing.T) {
	st := seedDB(t)
	ctx := skipShort(t)

	res, err := st.InspectServer(ctx)
	require.NoError(t, err)
	// A CouchDB 2.1+ server reports a non-empty feature list, which the driver
	// always surfaces (an empty list on an older server).
	require.NotEmpty(t, res.(map[string]any)["features"])
}

func TestFormatRawFallsBackOnUnserializable(t *testing.T) {
	// A value JSON cannot render (a channel) exercises the FormatRaw error branch:
	// it must fall back to a plain string rather than return empty or panic. No
	// server is needed.
	st := &Store{}
	out := st.FormatRaw(make(chan int), false)
	require.NotEmpty(t, out)
}

// compile-time assertion that Store satisfies the write capabilities, so a
// signature drift is caught at build time rather than only by the wiring.
var (
	_ query.Putter      = (*Store)(nil)
	_ query.Clearer     = (*Store)(nil)
	_ query.Dropper     = (*Store)(nil)
	_ query.TypedReader = (*Store)(nil)
	_ query.Estimator   = (*Store)(nil)
)

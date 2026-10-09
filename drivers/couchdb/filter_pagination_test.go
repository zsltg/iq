package couchdb

import (
	"context"
	"testing"

	"github.com/go-kivik/kivik/v4/driver"
	"github.com/go-kivik/kivik/v4/mockdb"
	"github.com/stretchr/testify/require"
)

// findRecorder answers a sequence of _find requests and records each one.
type findRecorder struct {
	t       *testing.T
	replies []driver.Rows
	queries []any
}

// answer is the WillExecute callback. It records the request and returns the next reply.
func (f *findRecorder) answer(ctx context.Context, query any, _ driver.Options) (driver.Rows, error) {
	requireKeyed(f.t, ctx)
	f.queries = append(f.queries, query)
	reply := f.replies[len(f.queries)-1]
	return reply, nil
}

// expect registers one expectation for each reply.
func (f *findRecorder) expect(mdb *mockdb.DB) {
	for range f.replies {
		mdb.ExpectFind().WillExecute(f.answer)
	}
}

func TestFindPagedSendsTheSelectorLimitAndBookmark(t *testing.T) {
	// Page one asks for the selector and the limit only. Page two adds the bookmark
	// that page one returned. Every request carries the caller's context.
	st, _, mdb := newMockStore(t, 2, 1)
	rec := &findRecorder{t: t, replies: []driver.Rows{
		withBookmark(mockdb.NewRows().
			AddRow(docRow("1", `{"_id":"1"}`)).
			AddRow(docRow("2", `{"_id":"2"}`)), "bm-1"),
		withBookmark(mockdb.NewRows().AddRow(docRow("3", `{"_id":"3"}`)), "bm-2"),
	}}
	rec.expect(mdb)
	sel := map[string]any{"a": 1}

	var sizes []int
	require.NoError(t, st.findPaged(keyedCtx(), sel, func(batch map[string]any) error {
		sizes = append(sizes, len(batch))
		return nil
	}))

	require.Equal(t, []int{2, 1}, sizes)
	require.Len(t, rec.queries, 2)
	wantSel := map[string]any{"a": float64(1)}
	require.Equal(t, map[string]any{"selector": wantSel, "limit": float64(2)}, queryMap(t, rec.queries[0]))
	require.Equal(t, map[string]any{"selector": wantSel, "limit": float64(2), "bookmark": "bm-1"}, queryMap(t, rec.queries[1]))
}

func TestFindPagedCountsDesignDocumentsButDoesNotHandThemOn(t *testing.T) {
	// A design document fills a slot of the page, so a page with one design
	// document and one user document is full and the walk goes on. A page of design
	// documents only is never handed to the caller.
	st, _, mdb := newMockStore(t, 2, 1)
	rec := &findRecorder{t: t, replies: []driver.Rows{
		withBookmark(mockdb.NewRows().
			AddRow(docRow("_design/idx", `{"_id":"_design/idx"}`)).
			AddRow(docRow("1", `{"_id":"1"}`)), "bm-1"),
		withBookmark(mockdb.NewRows().AddRow(docRow("_design/two", `{"_id":"_design/two"}`)), "bm-2"),
	}}
	rec.expect(mdb)

	var pages []map[string]any
	require.NoError(t, st.findPaged(keyedCtx(), map[string]any{"a": 1}, func(batch map[string]any) error {
		pages = append(pages, batch)
		return nil
	}))

	require.Len(t, rec.queries, 2)
	require.Equal(t, []map[string]any{{"1": map[string]any{"_id": "1"}}}, pages)
}

func TestFindPagedStopsAfterAShortPageThatHasABookmark(t *testing.T) {
	// A page shorter than the limit ends the walk even when the server sent a
	// bookmark. The mock holds one reply, so a second request would fail.
	st, _, mdb := newMockStore(t, 2, 1)
	rec := &findRecorder{t: t, replies: []driver.Rows{
		withBookmark(mockdb.NewRows().AddRow(docRow("1", `{"_id":"1"}`)), "bm-1"),
	}}
	rec.expect(mdb)

	pages := 0
	require.NoError(t, st.findPaged(keyedCtx(), map[string]any{"a": 1}, func(map[string]any) error {
		pages++
		return nil
	}))

	require.Equal(t, 1, pages)
	require.Len(t, rec.queries, 1)
}

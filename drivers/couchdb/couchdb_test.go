package couchdb

import (
	"bytes"
	"context"
	"maps"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
)

// booksDocs is the shared example dataset: four books keyed by string _id "1".."4".
func booksDocs() []map[string]any {
	return []map[string]any{
		{"_id": "1", "title": "The Go Programming Language", "author": "Donovan and Kernighan", "year": 2015},
		{"_id": "2", "title": "Designing Data-Intensive Applications", "author": "Martin Kleppmann", "year": 2017},
		{"_id": "3", "title": "A Philosophy of Software Design", "author": "John Ousterhout", "year": 2018},
		{"_id": "4", "title": "Clean Code", "author": "Robert C. Martin", "year": 2008},
	}
}

// collect drains a scan into one map, the shape ScanBatches/ScanFiltered hand pages in.
func collect(t *testing.T, scan func(func(map[string]any) error) error) map[string]any {
	t.Helper()
	out := map[string]any{}
	require.NoError(t, scan(func(batch map[string]any) error {
		maps.Copy(out, batch)
		return nil
	}))
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestGet(t *testing.T) {
	st := seedDB(t, booksDocs()...)
	ctx := skipShort(t)

	got, err := st.Get(ctx, []string{"2", "99"})
	require.NoError(t, err)

	doc, ok := got["2"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Martin Kleppmann", doc["author"])
	require.NotContains(t, got, "99", "a missing key is absent from the map")
}

func TestScanBatchesPaginates(t *testing.T) {
	st := seedDB(t, booksDocs()...)
	st.pageSize = 2 // force the startkey cursor across multiple pages
	ctx := skipShort(t)

	got := collect(t, func(fn func(map[string]any) error) error { return st.ScanBatches(ctx, fn) })
	require.Equal(t, []string{"1", "2", "3", "4"}, sortedKeys(got))
}

func TestScanBatchesSkipsDesignDocs(t *testing.T) {
	docs := append(booksDocs(), map[string]any{"_id": "_design/idx", "views": map[string]any{}})
	st := seedDB(t, docs...)
	ctx := skipShort(t)

	got := collect(t, func(fn func(map[string]any) error) error { return st.ScanBatches(ctx, fn) })
	require.Equal(t, []string{"1", "2", "3", "4"}, sortedKeys(got), "the _design document is not a data document")
}

func TestScanFilteredPushesEquality(t *testing.T) {
	st := seedDB(t, booksDocs()...)
	ctx := skipShort(t)

	got := collect(t, func(fn func(map[string]any) error) error {
		return st.ScanFiltered(ctx, predicate.Eq{Path: []string{"author"}, Value: "Martin Kleppmann"}, fn)
	})
	require.Equal(t, []string{"2"}, sortedKeys(got))
}

func TestScanFilteredPushesRange(t *testing.T) {
	st := seedDB(t, booksDocs()...)
	ctx := skipShort(t)

	got := collect(t, func(fn func(map[string]any) error) error {
		return st.ScanFiltered(ctx, predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2015.0}, fn)
	})
	require.Equal(t, []string{"2", "3"}, sortedKeys(got))
}

func TestScanFilteredPushesRegex(t *testing.T) {
	st := seedDB(
		t,
		map[string]any{"_id": "1", "name": "Ångström"}, // non-ASCII subject containing "str"
		map[string]any{"_id": "2", "name": "kelvin"},   // lowercase
		map[string]any{"_id": "3", "name": "Kelvin"},   // ASCII K
		map[string]any{"_id": "4", "name": "meter"},    // no match
		map[string]any{"_id": "5", "name": 42},         // non-string: is_binary guard skips it
		map[string]any{"_id": "6", "name": "Kelvin"},   // Kelvin sign (U+212A), not ASCII K
	)
	ctx := skipShort(t)

	// An ASCII, byte-safe pattern matches the same bytes CouchDB's byte-mode $regex
	// sees, even over a non-ASCII subject: "Ångström" contains the bytes "str".
	got := collect(t, func(fn func(map[string]any) error) error {
		return st.ScanFiltered(ctx, predicate.Regex{Path: []string{"name"}, Pattern: "str"}, fn)
	})
	require.Equal(t, []string{"1"}, sortedKeys(got))

	// Case-sensitive ^K matches ASCII "Kelvin" only: not lowercase "kelvin", not the
	// Kelvin sign (a different rune), and not the number field (is_binary skips it),
	// exactly as jq's test would — the i flag is never pushed.
	got = collect(t, func(fn func(map[string]any) error) error {
		return st.ScanFiltered(ctx, predicate.Regex{Path: []string{"name"}, Pattern: "^K"}, fn)
	})
	require.Equal(t, []string{"3"}, sortedKeys(got))
}

func TestScanFilteredPushesSize(t *testing.T) {
	st := seedDB(
		t,
		map[string]any{"_id": "1", "tags": []any{"a", "b"}},                // array length 2 — a true match
		map[string]any{"_id": "2", "tags": []any{"a"}},                     // array length 1 — never matched
		map[string]any{"_id": "3", "tags": "xyz"},                          // string — in the superset, not a true match
		map[string]any{"_id": "4", "tags": map[string]any{"a": 1, "b": 2}}, // object — in the superset
	)
	ctx := skipShort(t)

	// jq length is polymorphic, so the push is a $size-plus-$type superset: the
	// length-2 array, plus every string/object/number regardless of length. It must
	// never miss the array (doc 1); the client re-run narrows the rest.
	got := collect(t, func(fn func(map[string]any) error) error {
		return st.ScanFiltered(ctx, predicate.Size{Path: []string{"tags"}, N: 2}, fn)
	})
	require.Equal(t, []string{"1", "3", "4"}, sortedKeys(got))
}

func TestScanFilteredFallsBackForNonPushable(t *testing.T) {
	st := seedDB(t, booksDocs()...)
	ctx := skipShort(t)

	// A != predicate does not narrow, so ScanFiltered must fall back to a full scan
	// (a superset) rather than trust an unsafe selector.
	got := collect(t, func(fn func(map[string]any) error) error {
		return st.ScanFiltered(ctx, predicate.Ne{Path: []string{"author"}, Value: "Martin Kleppmann"}, fn)
	})
	require.Equal(t, []string{"1", "2", "3", "4"}, sortedKeys(got))
}

func TestQueryRawFind(t *testing.T) {
	st := seedDB(t, booksDocs()...)
	ctx := skipShort(t)

	res, err := st.Query(ctx, []string{`{"selector": {"year": {"$gt": 2015}}}`})
	require.NoError(t, err)
	m, ok := res.(map[string]any)
	require.True(t, ok)
	docs, ok := m["docs"].([]any)
	require.True(t, ok)
	require.Len(t, docs, 2)
}

func TestQueryRawBareSelectorIsWrapped(t *testing.T) {
	st := seedDB(t, booksDocs()...)
	ctx := skipShort(t)

	res, err := st.Query(ctx, []string{`{"author": "Robert C. Martin"}`})
	require.NoError(t, err)
	docs := res.(map[string]any)["docs"].([]any)
	require.Len(t, docs, 1)
}

func TestEstimateCount(t *testing.T) {
	st := seedDB(t, booksDocs()...)
	ctx := skipShort(t)

	n, err := st.EstimateCount(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(4), n)
}

func TestInspect(t *testing.T) {
	st := seedDB(t, booksDocs()...)
	ctx := skipShort(t)

	server, err := st.InspectServer(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, server.(map[string]any)["version"])

	dbs, err := st.InspectDatabases(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, dbs.(map[string]any)["databases"])

	info, err := st.InspectDBInfo(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(4), info.(map[string]any)["docCount"])

	_, err = st.InspectIndexes(ctx)
	require.NoError(t, err)
}

func TestTraceRedactsCredential(t *testing.T) {
	ctx := skipShort(t)
	name := dbName(t)
	client := adminClient(t)
	_ = client.DestroyDB(ctx, name)
	require.NoError(t, client.CreateDB(ctx, name))
	t.Cleanup(func() { _ = client.DestroyDB(context.Background(), name) })

	var buf bytes.Buffer
	st, err := Open(ctx, testURL(), name, &buf, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	_, err = st.Get(ctx, []string{"1"})
	require.NoError(t, err)

	trace := buf.String()
	require.Contains(t, trace, "couch>", "the request trace is written")
	require.NotContains(t, trace, couchPassword, "the credential is never traced")
}

func TestNoDatabaseSelected(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping couchdb integration test in -short mode")
	}
	// A server-scoped store (no database) rejects database-scoped reads with a clear
	// sentinel rather than a driver error.
	st := openIntegration(t, "")
	ctx := skipShort(t)

	_, err := st.Get(ctx, []string{"1"})
	require.ErrorIs(t, err, errNoDatabase)

	err = st.ScanBatches(ctx, func(map[string]any) error { return nil })
	require.ErrorIs(t, err, errNoDatabase)
}

func TestOpenBadServerFailsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping couchdb integration test in -short mode")
	}
	ctx := skipShort(t)
	_, err := Open(ctx, "couchdb://127.0.0.1:1/", "iq", nil, numfmt.DecimalAuto)
	require.Error(t, err)
	require.Contains(t, err.Error(), "connect couchdb")
}

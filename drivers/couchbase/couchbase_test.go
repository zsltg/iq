package couchbase

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/couchbase/gocb/v2"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

// books returns a small fixture keyed by document ID, using []any and int values so a
// round-trip through JSON compares equal to what the driver decodes.
func books() map[string]map[string]any {
	return map[string]map[string]any{
		"1": {"title": "The Go Programming Language", "year": 2015, "price": 39, "tags": []any{"go", "programming"}},
		"2": {"title": "Designing Data-Intensive Applications", "year": 2017, "price": 45, "tags": []any{"data"}},
		"3": {"title": "A Philosophy of Software Design", "year": 2018, "price": 20, "tags": []any{"design"}},
	}
}

func TestGet(t *testing.T) {
	fixture := books()
	st := seedCollectionKV(t, fixture)
	ctx := skipShort(t)

	out, err := st.Get(ctx, []string{"1", "3", "missing"})
	require.NoError(t, err)
	require.Equal(t, fixture["1"], out["1"])
	require.Equal(t, fixture["3"], out["3"])
	require.NotContains(t, out, "missing", "a key with no document is absent from the map")
}

func TestGetEmpty(t *testing.T) {
	st := seedCollectionKV(t, books())
	ctx := skipShort(t)
	out, err := st.Get(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestGetKeyValidation(t *testing.T) {
	st := seedCollectionKV(t, books())
	ctx := skipShort(t)
	_, err := st.Get(ctx, []string{""})
	require.Error(t, err)
}

func TestScanBatches(t *testing.T) {
	fixture := books()
	st := seedCollection(t, fixture)
	ctx := skipShort(t)
	st.pageSize = 2 // force multiple pages over three documents

	got := map[string]any{}
	pages := 0
	require.NoError(t, st.ScanBatches(ctx, func(batch map[string]any) error {
		pages++
		require.LessOrEqual(t, len(batch), st.pageSize)
		for k, v := range batch {
			got[k] = v
		}
		return nil
	}))
	require.Greater(t, pages, 1, "small page size should yield multiple pages")
	require.Len(t, got, len(fixture))
	require.Equal(t, fixture["2"], got["2"])
}

// TestScanBatchesContextCancelled pins that the context ScanBatches is handed flows all
// the way into the gocb query execution: a context cancelled before the scan starts must
// surface the cancellation instead of being ignored. It uses an explicit Cancel (never a
// timeout duration) so the cancellation is deterministic and immediate.
func TestScanBatchesContextCancelled(t *testing.T) {
	st := seedCollection(t, books())
	ctx := skipShort(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel() // cancel before the scan issues its first query

	err := st.ScanBatches(cctx, func(map[string]any) error { return nil })
	require.Error(t, err, "a cancelled context must surface as an error, not a silent full scan")
	// gocb surfaces a cancelled request as its own sentinel (ErrRequestCanceled), not a
	// wrapped context.Canceled, so match the driver's actual error rather than ctx.Err().
	require.ErrorIs(t, err, gocb.ErrRequestCanceled)
}

func TestScanBatchesEmpty(t *testing.T) {
	st := seedCollection(t, nil) // an indexed but empty collection
	ctx := skipShort(t)

	calls := 0
	require.NoError(t, st.ScanBatches(ctx, func(map[string]any) error {
		calls++
		return nil
	}))
	require.Zero(t, calls, "an empty collection must never hand fn a page")
}

func TestQuery(t *testing.T) {
	st := seedCollection(t, books())
	ctx := skipShort(t)
	ref := st.keyspaceRef()

	rows, err := st.Query(ctx, []string{
		"SELECT RAW t.title FROM " + ref + " t WHERE t.year >= $min ORDER BY t.year",
		`{"min": 2017}`,
	})
	require.NoError(t, err)
	require.Equal(t, []any{"Designing Data-Intensive Applications", "A Philosophy of Software Design"}, rows)
}

func TestQueryScopeQualified(t *testing.T) {
	st := seedCollection(t, books())
	ctx := skipShort(t)

	// A bare (unqualified) collection name resolves only under a scope-qualified query
	// (Scope.Query), never Cluster.Query — so this pins that a bucket-selected source
	// runs statements against its scope.
	rows, err := st.Query(ctx, []string{"SELECT RAW COUNT(*) FROM `" + collName(t) + "`"})
	require.NoError(t, err)
	require.Equal(t, []any{len(books())}, rows)
}

func TestClose(t *testing.T) {
	ctx := skipShort(t)
	st, err := Open(ctx, testURL(), collName(t), nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	require.NoError(t, st.Close(), "closing a healthy store returns no error")
}

func TestQueryArgErrors(t *testing.T) {
	st := seedCollection(t, books())
	ctx := skipShort(t)

	_, err := st.Query(ctx, nil)
	require.Error(t, err)
	_, err = st.Query(ctx, []string{"SELECT 1", "{}", "extra"})
	require.Error(t, err)
	_, err = st.Query(ctx, []string{"SELECT 1", "not json"})
	require.Error(t, err)
}

func TestOpenNoBucket(t *testing.T) {
	ctx := skipShort(t)
	// sharedBase carries no ?bucket=, so the Store opens with no keyspace selected.
	st, err := Open(ctx, sharedBase, "", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	_, err = st.Get(ctx, []string{"1"})
	require.ErrorIs(t, err, errNoBucket)
	err = st.ScanBatches(ctx, func(map[string]any) error { return nil })
	require.ErrorIs(t, err, errNoBucket)
}

func TestOpenBadScheme(t *testing.T) {
	_, err := Open(context.Background(), "mongodb://localhost/?bucket=iq", "", nil, numfmt.DecimalAuto)
	require.Error(t, err)
}

func TestOpenUnreachable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping couchbase integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := Open(ctx, "couchbase://Administrator:password@127.0.0.1:1/?bucket=iq_test", "", nil, numfmt.DecimalAuto)
	require.Error(t, err)
}

func TestTraceRedaction(t *testing.T) {
	ctx := skipShort(t)
	var buf bytes.Buffer
	st, err := Open(ctx, testURL(), collName(t), &buf, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	_, _ = st.Get(ctx, []string{"k1", "k2"})

	trace := buf.String()
	require.Contains(t, trace, "couchbase> get k1 k2")
	require.NotContains(t, trace, "password")
}

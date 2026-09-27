package couchbase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/couchbase/gocb/v2"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
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

	// The absent key comes first: a not-found document must skip to the next key, never
	// end the walk, so the two documents behind it still arrive.
	out, err := st.Get(ctx, []string{"missing", "1", "3"})
	require.NoError(t, err)
	require.Equal(t, fixture["1"], out["1"])
	require.Equal(t, fixture["3"], out["3"])
	require.NotContains(t, out, "missing", "a key with no document is absent from the map")
}

func TestGetEmpty(t *testing.T) {
	st := seedCollectionKV(t, books())
	ctx := skipShort(t)
	var buf bytes.Buffer
	st.trace = &buf

	out, err := st.Get(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, out, "empty keys give an empty map, never a nil map")
	require.Empty(t, out)
	// The trace line comes after the short-circuit, so an empty trace proves the driver
	// made no round trip.
	require.Empty(t, buf.String(), "empty keys must not reach the cluster")
}

func TestGetKeyValidation(t *testing.T) {
	st := seedCollectionKV(t, books())
	ctx := skipShort(t)

	_, err := st.Get(ctx, []string{""})
	require.ErrorContains(t, err, "couchbase document key is empty")

	_, err = st.Get(ctx, []string{strings.Repeat("k", maxKeyBytes+1)})
	require.ErrorContains(t, err, "couchbase document key exceeds")
}

// TestGetOnAMissingCollection drives the per-document error path: a key sent to a
// collection that does not exist fails with a KV error that is not "document not found",
// so Get must report it and keep the cause reachable through the wrap.
func TestGetOnAMissingCollection(t *testing.T) {
	ctx := skipShort(t)
	st, err := Open(ctx, testURL(), "iq_no_such_collection", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	_, err = st.Get(ctx, []string{"1"})
	require.ErrorContains(t, err, "couchbase get")
	require.ErrorIs(t, err, gocb.ErrTimeout, "the KV cause must stay reachable through the wrap")
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
		maps.Copy(got, batch)
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

// TestKVBulkContextCancelled pins that the caller's context bounds a KV batch. gocb's core
// KV provider reads BulkOpOptions.Timeout only and ignores BulkOpOptions.Context, so the
// driver converts a deadline and refuses a context that is already done. Without that work
// a cancelled caller still completes the read or the write, because the batch obeys the SDK
// default alone. The scan checks cover the query path, which honours the context itself;
// this covers the KV path, which they never reach. The driver stops before it calls the
// SDK, so the error is context.Canceled and not the gocb sentinel.
func TestKVBulkContextCancelled(t *testing.T) {
	st := seedCollectionKV(t, books())
	ctx := skipShort(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel() // cancel before any batch issues its request

	tests := []struct {
		name string
		op   func() error
	}{
		{name: "get", op: func() error { _, err := st.Get(cctx, []string{"1"}); return err }},
		{name: "put upsert", op: func() error {
			_, err := st.Put(cctx, []query.Record{{Key: "9", Value: map[string]any{"n": 9}}}, query.Upsert)
			return err
		}},
		{name: "put insert only", op: func() error {
			_, err := st.Put(cctx, []query.Record{{Key: "8", Value: map[string]any{"n": 8}}}, query.InsertOnly)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorIs(t, tt.op(), context.Canceled,
				"a cancelled context must stop the batch, not complete it")
		})
	}
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
	// The predicate is necessary. Without a WHERE clause the planner answers COUNT(*)
	// with a CountScan, which reads the collection's KV document count. That count
	// updates later than the upserts and ignores RequestPlus, so the test got 1 of 3
	// documents about once in 15 runs. With the predicate the count goes through the
	// primary index, and RequestPlus makes it wait for the seeded documents.
	rows, err := st.Query(ctx, []string{"SELECT RAW COUNT(*) FROM `" + collName(t) + "` t WHERE t.year IS NOT MISSING"})
	require.NoError(t, err)
	require.Equal(t, []any{len(books())}, rows)
}

func TestClose(t *testing.T) {
	ctx := skipShort(t)
	st, err := Open(ctx, testURL(), collName(t), nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	require.NoError(t, st.Close(), "closing a healthy store returns no error")
	// A second close finds the cluster already shut down. The SDK reports it, and the
	// driver must pass that report on instead of hiding it.
	err = st.Close()
	require.ErrorContains(t, err, "close couchbase")
	// The message alone does not prove the wrap: %v and %w give the same text. The SDK
	// error must stay reachable as the cause.
	require.Error(t, errors.Unwrap(err), "close must keep the SDK error as its cause")
}

func TestQueryArgErrors(t *testing.T) {
	st := seedCollection(t, books())
	ctx := skipShort(t)

	_, err := st.Query(ctx, nil)
	require.ErrorContains(t, err, "couchbase raw expects a SQL++ statement")
	_, err = st.Query(ctx, []string{"SELECT 1", "{}", "extra"})
	require.ErrorContains(t, err, "couchbase raw expects a SQL++ statement",
		"a third argument is refused by the arity guard, not by the parameter parser")

	_, err = st.Query(ctx, []string{"SELECT 1", "not json"})
	require.ErrorContains(t, err, "parse couchbase query parameters")
	var syntax *json.SyntaxError
	require.ErrorAs(t, err, &syntax, "the JSON cause must stay reachable through the wrap")
}

// TestQueryStreamError drives the streaming-error path: the query service answers HTTP
// 200 and reports the failure in the payload it streams, so the error surfaces only when
// the row stream is checked after the loop. A driver that reads the rows and never checks
// the stream returns an empty result and calls the query a success.
func TestQueryStreamError(t *testing.T) {
	ctx := skipShort(t)
	st := openIntegration(t, collName(t))

	// ARRAY_RANGE over 20 million elements exceeds the server's result-size limit. The
	// statement is valid, so the failure arrives mid-stream, not as a request error.
	_, err := st.Query(ctx, []string{"SELECT RAW n FROM ARRAY_RANGE(0, 20000000) AS n"})
	require.ErrorContains(t, err, "couchbase query")
}

// TestOpenCarriesTheDecimalMode pins that the decimal mode chosen at the composition root
// reaches every decode: in string mode a fractional number keeps its exact literal
// instead of becoming a float.
func TestOpenCarriesTheDecimalMode(t *testing.T) {
	seedCollectionKV(t, map[string]map[string]any{"1": {"price": 19.99}})
	ctx := skipShort(t)

	st, err := Open(ctx, testURL(), collName(t), nil, numfmt.DecimalString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	got, err := st.Get(ctx, []string{"1"})
	require.NoError(t, err)
	require.Equal(t, "19.99", got["1"].(map[string]any)["price"])
}

// TestClusterOptionsDisableTelemetry pins the SDK options the driver connects with.
// Application telemetry reports metrics to the cluster unless it is disabled, and the
// zero value of the config leaves it on, so the flag must be set. parseURL is pure, so
// this needs no cluster.
func TestClusterOptionsDisableTelemetry(t *testing.T) {
	cc, err := parseURL("couchbase://user:pass@localhost/?bucket=iq", "")
	require.NoError(t, err)

	opts := clusterOptions(cc)
	require.True(t, opts.AppTelemetryConfig.Disabled, "app telemetry must be disabled explicitly")
	auth, ok := opts.Authenticator.(gocb.PasswordAuthenticator)
	require.True(t, ok, "the source credentials become the password authenticator")
	require.Equal(t, "user", auth.Username)
	require.Equal(t, "pass", auth.Password)
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
	// The parse failure must stop Open there. A later failure reports a connection
	// problem, which hides the real cause from the user.
	_, err := Open(context.Background(), "mongodb://localhost/?bucket=iq", "", nil, numfmt.DecimalAuto)
	require.ErrorContains(t, err, "must use couchbase://")
}

func TestOpenUnreachable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping couchbase integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err := Open(ctx, "couchbase://Administrator:password@127.0.0.1:1/?bucket=iq_test", "", nil, numfmt.DecimalAuto)
	require.ErrorContains(t, err, "connect couchbase")
	// The readiness probe takes the caller's deadline, which is shorter than
	// connectTimeout. A probe that ignores the context runs to connectTimeout instead.
	require.Less(t, time.Since(start), connectTimeout, "the caller's deadline must bound the readiness probe")
}

// TestOpenUnreachableNoBucket pins that a cluster that never becomes ready fails Open
// even when no bucket is selected. The bucket probe is the only other failure point, so a
// source without one proves the cluster probe reports on its own.
func TestOpenUnreachableNoBucket(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping couchbase integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err := Open(ctx, "couchbase://Administrator:password@127.0.0.1:1/", "", nil, numfmt.DecimalAuto)
	require.ErrorContains(t, err, "connect couchbase")
	require.Less(t, time.Since(start), connectTimeout, "the caller's deadline must bound the readiness probe")
}

// TestOpenMissingBucket pins the bucket probe: the cluster is healthy, so only the bucket
// readiness check can fail, and Open must report that instead of handing back a store
// that is scoped to a keyspace which does not exist.
func TestOpenMissingBucket(t *testing.T) {
	ctx := skipShort(t)
	u, err := url.Parse(sharedBase)
	require.NoError(t, err)
	q := u.Query()
	q.Set("bucket", "iq_no_such_bucket")
	u.RawQuery = q.Encode()

	bctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := time.Now()
	st, err := Open(bctx, u.String(), "", nil, numfmt.DecimalAuto)
	if err == nil {
		_ = st.Close()
	}
	require.ErrorContains(t, err, "connect couchbase")
	require.Error(t, errors.Unwrap(err), "open must keep the SDK error as its cause")
	require.Less(t, time.Since(start), connectTimeout, "the caller's deadline must bound the bucket probe")
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

// TestTraceSites pins the trace line of every op that writes one. Get is covered by
// TestTraceRedaction; the scan, the raw query, the two bulk writes, the clear and the
// drop each have their own line, and every statement carries parameter placeholders
// only. The ops run in order because clear empties the collection and drop removes it.
func TestTraceSites(t *testing.T) {
	st := seedCollection(t, books())
	ctx := skipShort(t)
	var buf bytes.Buffer
	st.trace = &buf

	tests := []struct {
		name string
		op   func(t *testing.T)
		want string
	}{
		{
			name: "scan traces the keyset statement",
			op: func(t *testing.T) {
				require.NoError(t, st.ScanBatches(ctx, func(map[string]any) error { return nil }))
			},
			want: fmt.Sprintf(
				"couchbase> query SELECT META(t).id AS k, t AS v FROM %s t WHERE META(t).id > $after ORDER BY META(t).id LIMIT $page",
				st.keyspaceRef(),
			),
		},
		{
			name: "raw query traces the statement",
			op: func(t *testing.T) {
				_, err := st.Query(ctx, []string{"SELECT RAW 1"})
				require.NoError(t, err)
			},
			want: "couchbase> query SELECT RAW 1",
		},
		{
			name: "upsert traces the document count",
			op: func(t *testing.T) {
				_, err := st.Put(ctx, []query.Record{
					{Key: "7", Value: map[string]any{"n": 7}},
					{Key: "8", Value: map[string]any{"n": 8}},
				}, query.Upsert)
				require.NoError(t, err)
			},
			want: "couchbase> upsert 2 documents",
		},
		{
			name: "insert traces the document count",
			op: func(t *testing.T) {
				_, err := st.Put(ctx, []query.Record{{Key: "9", Value: map[string]any{"n": 9}}}, query.InsertOnly)
				require.NoError(t, err)
			},
			want: "couchbase> insert 1 documents",
		},
		{
			name: "clear traces the delete statement",
			op:   func(t *testing.T) { require.NoError(t, st.Clear(ctx)) },
			want: "couchbase> query DELETE FROM " + st.keyspaceRef(),
		},
		{
			name: "drop traces the collection it removes",
			op:   func(t *testing.T) { require.NoError(t, st.Drop(ctx)) },
			want: fmt.Sprintf("couchbase> drop collection %s.%s", st.scope, st.coll),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()
			tt.op(t)
			require.Contains(t, buf.String(), tt.want)
		})
	}
}

// TestOpsAfterClose drives every op against a closed store. The SDK refuses each call, so
// each op must report that failure with its own context. These are the error guards that
// the package cannot reach in any other way: a shared bulk-op guard (three identical
// sites, of which the gate can flag one), the query guards, and the four inspect guards.
func TestOpsAfterClose(t *testing.T) {
	st := seedCollectionKV(t, books())
	ctx := skipShort(t)
	require.NoError(t, st.Close())

	tests := []struct {
		name string
		op   func() error
		want string
	}{
		{
			name: "get reports the bulk failure",
			op:   func() error { _, err := st.Get(ctx, []string{"1"}); return err },
			want: "couchbase get",
		},
		{
			name: "upsert reports the pre-read failure",
			op: func() error {
				_, err := st.Put(ctx, []query.Record{{Key: "1", Value: map[string]any{"n": 1}}}, query.Upsert)
				return err
			},
			want: "couchbase pre-read",
		},
		{
			name: "upsert of a keyless record reports the write failure",
			op: func() error {
				// A keyless record needs no pre-read, so the bulk upsert is the first call
				// that can fail, and its own guard is the one under test.
				_, err := st.Put(ctx, []query.Record{{Value: map[string]any{"n": 1}}}, query.Upsert)
				return err
			},
			want: "couchbase upsert",
		},
		{
			name: "insert reports the write failure",
			op: func() error {
				_, err := st.Put(ctx, []query.Record{{Key: "2", Value: map[string]any{"n": 2}}}, query.InsertOnly)
				return err
			},
			want: "couchbase insert",
		},
		{name: "clear reports the query failure", op: func() error { return st.Clear(ctx) }, want: "couchbase clear"},
		{name: "drop reports the failure", op: func() error { return st.Drop(ctx) }, want: "couchbase drop"},
		{
			name: "inspect cluster reports the failure",
			op:   func() error { _, err := st.InspectCluster(ctx); return err },
			want: "couchbase inspect cluster",
		},
		{
			name: "inspect buckets reports the failure",
			op:   func() error { _, err := st.InspectBuckets(ctx); return err },
			want: "couchbase inspect buckets",
		},
		{
			name: "inspect collections reports the failure",
			op:   func() error { _, err := st.InspectCollections(ctx); return err },
			want: "couchbase inspect collections",
		},
		{
			name: "inspect indexes reports the failure",
			op:   func() error { _, err := st.InspectIndexes(ctx); return err },
			want: "couchbase inspect indexes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.op()
			require.ErrorContains(t, err, tt.want)
			// The message alone does not prove the wrap: %v and %w give the same text. The
			// SDK error must stay reachable as the cause.
			require.Error(t, errors.Unwrap(err), "the op must keep the SDK error as its cause")
		})
	}
}

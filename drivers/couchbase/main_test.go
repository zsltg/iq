package couchbase

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/couchbase/gocb/v2"
	"github.com/stretchr/testify/require"
	tccouchbase "github.com/testcontainers/testcontainers-go/modules/couchbase"

	"github.com/zsltg/iq/internal/numfmt"
)

// couchbaseImage is the pinned Couchbase Community Edition image the integration tests
// run against. The testcontainers couchbase module detects CE, rejects EE-only
// services, and provisions the cluster, bucket, and primary index.
const couchbaseImage = "couchbase:community-7.6.2"

// sharedBucketName is the one bucket the integration tests share; buckets are
// expensive, so per-test isolation is a fresh collection inside it, not a fresh bucket.
const sharedBucketName = "iq_test"

// sharedBase is the base couchbase:// URL (credentials in userinfo, ?bucket= set) the
// integration tests connect to: an ephemeral Couchbase container started once for the
// whole package, or the IQ_COUCHBASE_URL override. It stays empty when integration
// tests are skipped (-short).
var (
	sharedBase   string
	sharedBucket string
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	if override := os.Getenv("IQ_COUCHBASE_URL"); override != "" {
		base, bucket, err := splitBucket(override)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse IQ_COUCHBASE_URL: %v\n", err)
			return 1
		}
		sharedBase, sharedBucket = base, bucket
		return m.Run()
	}

	ctx := context.Background()
	bucket := tccouchbase.NewBucket(sharedBucketName).
		WithQuota(100).
		WithReplicas(0).
		WithFlushEnabled(true).
		WithPrimaryIndex(true)
	container, err := tccouchbase.Run(ctx, couchbaseImage, tccouchbase.WithBuckets(bucket))
	if err != nil {
		fmt.Fprintf(os.Stderr, "start couchbase container: %v\n", err)
		return 1
	}
	defer func() { _ = container.Terminate(ctx) }()

	connStr, err := container.ConnectionString(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "couchbase connection string: %v\n", err)
		return 1
	}
	// ConnectionString has no credentials; inject the admin userinfo the module set.
	u, err := url.Parse(connStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse couchbase connection string: %v\n", err)
		return 1
	}
	u.User = url.UserPassword(container.Username(), container.Password())
	sharedBase, sharedBucket = u.String(), sharedBucketName
	return m.Run()
}

// splitBucket peels the ?bucket= off an override URL into the base URL (bucket removed)
// and the bucket name, so the shared harness knows which bucket to create collections
// in. An override without a bucket is an error, since the keyspace tests need one.
func splitBucket(raw string) (base, bucket string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	q := u.Query()
	bucket = q.Get("bucket")
	if bucket == "" {
		return "", "", fmt.Errorf("IQ_COUCHBASE_URL must set ?bucket=")
	}
	q.Del("bucket")
	u.RawQuery = q.Encode()
	return u.String(), bucket, nil
}

// testURL returns the base couchbase:// URL (bucket added back as ?bucket=) for
// integration tests, so Open sees the shared bucket and the per-test collection rides
// as the dotted address.
func testURL() string {
	u, _ := url.Parse(sharedBase)
	q := u.Query()
	q.Set("bucket", sharedBucket)
	u.RawQuery = q.Encode()
	return u.String()
}

// skipShort skips an integration test under -short and returns a bounded context.
func skipShort(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping couchbase integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// adminCluster opens a raw gocb cluster against the test server, used by the harness to
// create collections, seed documents, and drop them (operations distinct from the
// Store under test).
func adminCluster(t *testing.T) *gocb.Cluster {
	t.Helper()
	cc, err := parseURL(testURL(), "")
	require.NoError(t, err)
	cluster, err := gocb.Connect(cc.connStr, gocb.ClusterOptions{
		Authenticator:      gocb.PasswordAuthenticator{Username: cc.username, Password: cc.password},
		AppTelemetryConfig: gocb.AppTelemetryConfig{Disabled: true},
	})
	require.NoError(t, err)
	require.NoError(t, cluster.WaitUntilReady(30*time.Second, nil))
	t.Cleanup(func() { _ = cluster.Close(nil) })
	return cluster
}

// seedCollection creates a fresh collection named after the test in the shared bucket,
// creates its primary index and waits for it online, seeds the supplied documents by
// KV upsert, registers a drop on cleanup, and returns a Store scoped to it. Buckets are
// expensive and collections are cheap, so each test gets its own collection. It skips
// under -short.
func seedCollection(t *testing.T, docs map[string]map[string]any) *Store {
	t.Helper()
	ctx := skipShort(t)
	name := collName(t)
	cluster := adminCluster(t)
	mgr := cluster.Bucket(sharedBucket).Collections()

	spec := gocb.CollectionSpec{Name: name, ScopeName: defaultScope}
	_ = mgr.DropCollection(spec, nil)
	require.NoError(t, mgr.CreateCollection(spec, &gocb.CreateCollectionOptions{Context: ctx}))
	t.Cleanup(func() { _ = mgr.DropCollection(spec, nil) })

	ref := fmt.Sprintf("`%s`.`%s`.`%s`", sharedBucket, defaultScope, name)
	// A freshly created collection is not immediately queryable; retry until the query
	// service sees it. CREATE PRIMARY INDEX is synchronous — the index is built and
	// online when the statement returns — so no separate online-poll is needed.
	requireEventually(t, ctx, func() error {
		_, err := cluster.Query("CREATE PRIMARY INDEX ON "+ref, &gocb.QueryOptions{Context: ctx})
		return err
	})

	col := cluster.Bucket(sharedBucket).Scope(defaultScope).Collection(name)
	for id, doc := range docs {
		_, err := col.Upsert(id, doc, &gocb.UpsertOptions{Context: ctx})
		require.NoError(t, err)
	}
	return openIntegration(t, name)
}

// seedCollectionKV creates a fresh collection named after the test and seeds it by KV
// upsert, without a primary index — the fast path for KV-only tests (Get/Put), which
// never run a SQL++ scan and so pay none of the query-service index propagation. It
// skips under -short.
func seedCollectionKV(t *testing.T, docs map[string]map[string]any) *Store {
	t.Helper()
	ctx := skipShort(t)
	name := collName(t)
	cluster := adminCluster(t)
	mgr := cluster.Bucket(sharedBucket).Collections()

	spec := gocb.CollectionSpec{Name: name, ScopeName: defaultScope}
	_ = mgr.DropCollection(spec, nil)
	require.NoError(t, mgr.CreateCollection(spec, &gocb.CreateCollectionOptions{Context: ctx}))
	t.Cleanup(func() { _ = mgr.DropCollection(spec, nil) })

	col := cluster.Bucket(sharedBucket).Scope(defaultScope).Collection(name)
	// A freshly created collection is not immediately usable for KV; retry until a probe
	// succeeds, then seed.
	requireEventually(t, ctx, func() error {
		_, err := col.Exists("__probe__", &gocb.ExistsOptions{Context: ctx})
		return err
	})
	for id, doc := range docs {
		_, err := col.Upsert(id, doc, &gocb.UpsertOptions{Context: ctx})
		require.NoError(t, err)
	}
	return openIntegration(t, name)
}

// openIntegration opens a collection-scoped Store against the test server (the
// collection rides as the dotted address) and registers cleanup.
func openIntegration(t *testing.T, collection string) *Store {
	t.Helper()
	ctx := skipShort(t)
	st, err := Open(ctx, testURL(), collection, nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// requireEventually retries fn until it succeeds or the context deadline passes,
// keeping the readiness-polling loops out of the driver's mutation surface.
func requireEventually(t *testing.T, ctx context.Context, fn func() error) {
	t.Helper()
	var last error
	for range 120 {
		if last = fn(); last == nil {
			return
		}
		select {
		case <-ctx.Done():
			require.NoError(t, last)
		case <-time.After(250 * time.Millisecond):
		}
	}
	require.NoError(t, last)
}

// collName derives a unique, Couchbase-legal collection name from the test name
// (letters, digits, and underscores, under the length limit).
func collName(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, r := range t.Name() {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	name := "iq_" + b.String()
	if len(name) > maxIdentBytes {
		name = name[:maxIdentBytes]
	}
	return name
}

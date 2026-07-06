package hbase

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tsuna/gohbase"
	"github.com/tsuna/gohbase/hrpc"

	"github.com/zsltg/iq/internal/numfmt"
)

// testURL returns the base hbase:// URL integration tests connect to, from
// IQ_HBASE_URL, or "" when unset. HBase native RPC requires the region server to be
// reachable at the hostname ZooKeeper advertises, which testcontainers' random port
// mapping breaks, so there is no ephemeral-container fallback: integration tests run
// against a fixed-port stack (docker compose up -d --wait) named by IQ_HBASE_URL.
func testURL() string {
	return os.Getenv("IQ_HBASE_URL")
}

// integrationOrSkip skips a test under -short or when no IQ_HBASE_URL is set, so the
// unit suite (go test -short) is fully dependency-free.
func integrationOrSkip(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping hbase integration test in -short mode")
	}
	u := testURL()
	if u == "" {
		t.Skip("skipping hbase integration test: set IQ_HBASE_URL to a running cluster (docker compose up -d --wait)")
	}
	return u
}

// adminClient builds a raw gohbase admin client against the test cluster, used by the
// harness to create and drop tables.
func adminClient(t *testing.T, rawURL string) gohbase.AdminClient {
	t.Helper()
	cc, err := parseURL(rawURL, "")
	require.NoError(t, err)
	return gohbase.NewAdminClient(cc.zkquorum, gohbase.ZookeeperRoot(cc.znode))
}

// createTable creates a table with the given column families, registers a drop on
// cleanup, and returns nothing — tests open their own Store against it. It waits
// briefly for the table to become available, since CreateTable is asynchronous.
func createTable(t *testing.T, rawURL, table string, families ...string) {
	t.Helper()
	admin := adminClient(t, rawURL)
	fam := make(map[string]map[string]string, len(families))
	for _, f := range families {
		fam[f] = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// Drop any leftover from a previous run, ignoring errors when it is absent.
	_ = admin.DisableTable(hrpc.NewDisableTable(ctx, []byte(table)))
	_ = admin.DeleteTable(hrpc.NewDeleteTable(ctx, []byte(table)))
	require.NoError(t, admin.CreateTable(hrpc.NewCreateTable(ctx, []byte(table), fam)))
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = admin.DisableTable(hrpc.NewDisableTable(c, []byte(table)))
		_ = admin.DeleteTable(hrpc.NewDeleteTable(c, []byte(table)))
	})
}

// openIntegration opens a table-scoped Store against the test cluster, with an
// optional URL query suffix (e.g. "&types=cf:n=long") appended for typed columns.
func openIntegration(t *testing.T, rawURL, table, querySuffix string) *Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := Open(ctx, rawURL+querySuffix, table, nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

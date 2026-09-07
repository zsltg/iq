package cassandra

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tccassandra "github.com/testcontainers/testcontainers-go/modules/cassandra"

	"github.com/zsltg/iq/internal/numfmt"
)

// testKeyspace is the keyspace the integration tests run against, created once so
// the tests stay off any real data.
const testKeyspace = "iq_test"

// sharedURL is the base cassandra:// URL (no table) the integration tests connect
// to: an ephemeral container started once for the whole package. It stays empty when
// integration tests are skipped (-short) or an external cluster is supplied
// (IQ_CASSANDRA_URL).
var sharedURL string

// TestMain provisions the test Cassandra before the suite runs. flag.Parse must run
// before testing.Short is read, and os.Exit skips deferred cleanup, so the work lives
// in runTests where the defer fires before the process exits.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	base := os.Getenv("IQ_CASSANDRA_URL")
	if base == "" {
		ctx := context.Background()
		container, err := tccassandra.Run(ctx, "cassandra:5")
		if err != nil {
			fmt.Fprintf(os.Stderr, "start cassandra container: %v\n", err)
			return 1
		}
		defer func() { _ = testcontainers.TerminateContainer(container) }()
		host, err := container.ConnectionHost(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cassandra connection host: %v\n", err)
			return 1
		}
		base = "cassandra://" + host + "/" + testKeyspace
	}
	if err := ensureKeyspace(base); err != nil {
		fmt.Fprintf(os.Stderr, "create cassandra keyspace: %v\n", err)
		return 1
	}
	sharedURL = base
	return m.Run()
}

// ensureKeyspace creates the test keyspace if it does not exist, connecting without a
// keyspace so the CREATE can run. It makes a freshly-started cluster ready for the
// table-scoped tests.
func ensureKeyspace(base string) error {
	cc, err := parseURL(base, "")
	if err != nil {
		return err
	}
	cluster := gocql.NewCluster(cc.hosts...)
	cluster.ConnectTimeout = 30 * time.Second
	cluster.Timeout = 30 * time.Second
	session, err := cluster.CreateSession()
	if err != nil {
		return err
	}
	defer session.Close()
	stmt := "CREATE KEYSPACE IF NOT EXISTS " + cc.keyspace +
		" WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}"
	return session.Query(stmt).Exec()
}

// testURL returns the base cassandra:// URL for integration tests: the
// IQ_CASSANDRA_URL override first, then the ephemeral container from TestMain, then a
// local default.
func testURL() string {
	if u := os.Getenv("IQ_CASSANDRA_URL"); u != "" {
		return u
	}
	if sharedURL != "" {
		return sharedURL
	}
	return "cassandra://localhost:9042/" + testKeyspace
}

// openIntegration skips under -short, otherwise opens a table-scoped Store against
// the test cluster and registers cleanup. The table must already exist.
func openIntegration(t *testing.T, table string) *Store {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping cassandra integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := Open(ctx, testURL(), table, nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// schemaStore opens a table-less Store for DDL and raw CQL, skipping under -short.
func schemaStore(t *testing.T) *Store {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping cassandra integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := Open(ctx, testURL(), "", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// openIntegrationMode is openIntegration with an explicit decimal mode, for the
// tests that assert the mode Open was given reaches the values it normalizes.
func openIntegrationMode(t *testing.T, table string, dec numfmt.DecimalMode) *Store {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping cassandra integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := Open(ctx, testURL(), table, nil, dec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// mustExec runs each CQL statement against st, failing the test on any error.
func mustExec(t *testing.T, st *Store, stmts ...string) {
	t.Helper()
	for _, stmt := range stmts {
		_, err := st.Query(context.Background(), []string{stmt})
		require.NoError(t, err, stmt)
	}
}

// seedTable drops any existing table of the given name, runs its DDL and seed
// inserts, registers a drop on cleanup, and returns a table-scoped Store for it. The
// DDL and inserts reference the table unqualified; the session keyspace is the test
// keyspace.
func seedTable(t *testing.T, name, ddl string, inserts ...string) *Store {
	t.Helper()
	schema := schemaStore(t)
	qualified := quoteIdent(testKeyspace) + "." + quoteIdent(name)
	mustExec(t, schema, "DROP TABLE IF EXISTS "+qualified)
	mustExec(t, schema, ddl)
	mustExec(t, schema, inserts...)
	t.Cleanup(func() {
		_, _ = schema.Query(context.Background(), []string{"DROP TABLE IF EXISTS " + qualified})
	})
	return openIntegration(t, name)
}

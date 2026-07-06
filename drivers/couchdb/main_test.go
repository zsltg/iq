package couchdb

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-kivik/kivik/v4"
	_ "github.com/go-kivik/kivik/v4/couchdb"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/zsltg/iq/internal/numfmt"
)

// couchImage is the pinned CouchDB image the integration tests run against, a generic
// container (there is no dedicated testcontainers module, and a generic one adds no
// new module dependency).
const couchImage = "couchdb:3"

// couchAuth is the admin credential the container is configured with; it also puts
// CouchDB into single-node mode.
const couchUser, couchPassword = "admin", "password"

// sharedURL is the base couchdb:// server URL (no database) the integration tests
// connect to: an ephemeral CouchDB container started once for the whole package. It
// stays empty when integration tests are skipped (-short) or an external server is
// supplied (IQ_COUCHDB_URL).
var sharedURL string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	base := os.Getenv("IQ_COUCHDB_URL")
	if base == "" {
		ctx := context.Background()
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        couchImage,
				ExposedPorts: []string{"5984/tcp"},
				Env: map[string]string{
					"COUCHDB_USER":     couchUser,
					"COUCHDB_PASSWORD": couchPassword,
				},
				WaitingFor: wait.ForHTTP("/_up").WithPort("5984/tcp").WithStartupTimeout(60 * time.Second),
			},
			Started: true,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "start couchdb container: %v\n", err)
			return 1
		}
		defer func() { _ = testcontainers.TerminateContainer(container) }()
		host, err := container.Host(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "couchdb container host: %v\n", err)
			return 1
		}
		port, err := container.MappedPort(ctx, "5984")
		if err != nil {
			fmt.Fprintf(os.Stderr, "couchdb container port: %v\n", err)
			return 1
		}
		base = fmt.Sprintf("couchdb://%s:%s@%s:%s/", couchUser, couchPassword, host, port.Port())
	}
	sharedURL = base
	return m.Run()
}

// testURL returns the base couchdb:// server URL for integration tests: the
// IQ_COUCHDB_URL override first, then the ephemeral container from TestMain, then a
// local default.
func testURL() string {
	if u := os.Getenv("IQ_COUCHDB_URL"); u != "" {
		return u
	}
	if sharedURL != "" {
		return sharedURL
	}
	return fmt.Sprintf("couchdb://%s:%s@localhost:5984/", couchUser, couchPassword)
}

// adminClient builds a raw kivik client against the test server, used by the harness
// to create, seed, and drop databases (operations distinct from the Store under test).
func adminClient(t *testing.T) *kivik.Client {
	t.Helper()
	cc, err := parseURL(testURL(), "")
	require.NoError(t, err)
	client, err := kivik.New("couch", cc.dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// skipShort skips an integration test under -short and returns a bounded context.
func skipShort(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping couchdb integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// seedDB creates a fresh database named after the test, inserts the supplied
// documents (each a map, keyed by its _id), registers a drop on cleanup, and returns
// a database-scoped Store for it. It skips under -short.
func seedDB(t *testing.T, docs ...map[string]any) *Store {
	t.Helper()
	ctx := skipShort(t)
	name := dbName(t)
	client := adminClient(t)

	_ = client.DestroyDB(ctx, name)
	require.NoError(t, client.CreateDB(ctx, name))
	t.Cleanup(func() { _ = client.DestroyDB(context.Background(), name) })

	if len(docs) > 0 {
		anyDocs := make([]any, len(docs))
		for i, d := range docs {
			anyDocs[i] = d
		}
		_, err := client.DB(name).BulkDocs(ctx, anyDocs)
		require.NoError(t, err)
	}
	return openIntegration(t, name)
}

// openIntegration opens a database-scoped Store against the test server and registers
// cleanup. The database must already exist.
func openIntegration(t *testing.T, db string) *Store {
	t.Helper()
	ctx := skipShort(t)
	st, err := Open(ctx, testURL(), db, nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// dbName derives a unique, lowercase, CouchDB-legal database name from the test name
// (CouchDB requires lowercase and forbids most punctuation).
func dbName(t *testing.T) string {
	t.Helper()
	var b []rune
	for _, r := range t.Name() {
		switch {
		case r >= 'A' && r <= 'Z':
			b = append(b, r+('a'-'A'))
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b = append(b, r)
		default:
			b = append(b, '_')
		}
	}
	return "iq_test_" + string(b)
}

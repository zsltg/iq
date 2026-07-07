package elasticsearch

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// esImage is the pinned Elasticsearch image the integration tests run against, a
// generic container (there is no dedicated testcontainers module in use, and a
// generic one adds no new module dependency).
const esImage = "docker.elastic.co/elasticsearch/elasticsearch:8.17.4"

// sharedURL is the base elasticsearch:// server URL (no index) the integration tests
// connect to: an ephemeral Elasticsearch container started once for the whole
// package. It stays empty when integration tests are skipped (-short) or an external
// server is supplied (IQ_ELASTICSEARCH_URL).
var sharedURL string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	base := os.Getenv("IQ_ELASTICSEARCH_URL")
	if base == "" {
		ctx := context.Background()
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        esImage,
				ExposedPorts: []string{"9200/tcp"},
				Env: map[string]string{
					"discovery.type":         "single-node",
					"xpack.security.enabled": "false",
					"ES_JAVA_OPTS":           "-Xms512m -Xmx512m",
				},
				WaitingFor: wait.ForHTTP("/_cluster/health").
					WithPort("9200/tcp").
					WithStartupTimeout(180 * time.Second),
			},
			Started: true,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "start elasticsearch container: %v\n", err)
			return 1
		}
		defer func() { _ = testcontainers.TerminateContainer(container) }()
		host, err := container.Host(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "elasticsearch container host: %v\n", err)
			return 1
		}
		port, err := container.MappedPort(ctx, "9200")
		if err != nil {
			fmt.Fprintf(os.Stderr, "elasticsearch container port: %v\n", err)
			return 1
		}
		base = fmt.Sprintf("elasticsearch://%s:%s/", host, port.Port())
	}
	sharedURL = base
	return m.Run()
}

// testURL returns the base elasticsearch:// server URL for integration tests: the
// IQ_ELASTICSEARCH_URL override first, then the ephemeral container from TestMain,
// then a local default.
func testURL() string {
	if u := os.Getenv("IQ_ELASTICSEARCH_URL"); u != "" {
		return u
	}
	if sharedURL != "" {
		return sharedURL
	}
	return "elasticsearch://localhost:9200/"
}

// skipShort skips an integration test under -short and returns a bounded context.
func skipShort(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping elasticsearch integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// seedIndex creates a fresh index named after the test, indexes the supplied
// documents (each a map keyed by its "_id"), registers a drop on cleanup, and returns
// an index-scoped Store opened after the seed so it reads the populated mapping. It
// skips under -short. Passing no documents yields an empty index.
func seedIndex(t *testing.T, docs ...map[string]any) *Store {
	t.Helper()
	ctx := skipShort(t)
	name := indexName(t)

	// An admin store seeds and drops the index; it is opened before the index exists,
	// so it carries no mapping (which only the store under test needs).
	admin, err := Open(ctx, testURL(), name, nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })

	_ = admin.Drop(ctx) // clean slate; ignore a not-found.
	t.Cleanup(func() { _ = admin.Drop(context.Background()) })

	if len(docs) > 0 {
		recs := make([]query.Record, len(docs))
		for i, d := range docs {
			id, _ := d["_id"].(string)
			recs[i] = query.Record{Key: id, Type: "document", Value: d}
		}
		_, err := admin.Put(ctx, recs, query.Upsert)
		require.NoError(t, err)
	}

	// The store under test opens after the seed, so its one-time mapping read sees the
	// dynamically mapped fields the seed created (text+keyword, long, boolean).
	st, err := Open(ctx, testURL(), name, nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// indexName derives a unique, lowercase, Elasticsearch-legal index name from the test
// name (Elasticsearch requires lowercase and forbids most punctuation).
func indexName(t *testing.T) string {
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

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
	"github.com/zsltg/iq/internal/testimage"
)

// sharedURL and sharedOSURL are the base server URLs (no index) the integration tests
// connect to: ephemeral Elasticsearch and OpenSearch containers started once for the
// whole package. They stay empty when integration tests are skipped (-short) or an
// external server is supplied (IQ_ELASTICSEARCH_URL / IQ_OPENSEARCH_URL).
var (
	sharedURL   string
	sharedOSURL string
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	// Elasticsearch backend: an external server (IQ_ELASTICSEARCH_URL) or an ephemeral
	// container. Security is disabled so the client connects over plain HTTP.
	esBase := os.Getenv("IQ_ELASTICSEARCH_URL")
	if esBase == "" {
		image, err := testimage.Ref("elasticsearch")
		if err != nil {
			fmt.Fprintf(os.Stderr, "elasticsearch image: %v\n", err)
			return 1
		}
		url, terminate, err := startContainer(image, "elasticsearch", map[string]string{
			"discovery.type":         "single-node",
			"xpack.security.enabled": "false",
			"ES_JAVA_OPTS":           "-Xms512m -Xmx512m",
			// A CI runner's disk sits past the flood-stage watermark after a few
			// image pulls, which leaves new shards unassigned and every write
			// waiting; the container is ephemeral, so the decider is off.
			"cluster.routing.allocation.disk.threshold_enabled": "false",
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "start elasticsearch container: %v\n", err)
			return 1
		}
		defer terminate()
		esBase = url
	}
	sharedURL = esBase

	// OpenSearch backend: likewise, with the security plugin disabled.
	osBase := os.Getenv("IQ_OPENSEARCH_URL")
	if osBase == "" {
		image, err := testimage.Ref("opensearch")
		if err != nil {
			fmt.Fprintf(os.Stderr, "opensearch image: %v\n", err)
			return 1
		}
		url, terminate, err := startContainer(image, "opensearch", map[string]string{
			"discovery.type":          "single-node",
			"DISABLE_SECURITY_PLUGIN": "true",
			"OPENSEARCH_JAVA_OPTS":    "-Xms512m -Xmx512m",
			// Same as above; past the flood stage OpenSearch blocks index creation.
			"cluster.routing.allocation.disk.threshold_enabled": "false",
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "start opensearch container: %v\n", err)
			return 1
		}
		defer terminate()
		osBase = url
	}
	sharedOSURL = osBase

	return m.Run()
}

// startContainer starts a generic Elasticsearch- or OpenSearch-compatible container that
// exposes 9200 and answers /_cluster/health, and returns the base URL (with the given
// scheme) plus a terminate func.
func startContainer(image, scheme string, env map[string]string) (string, func(), error) {
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Image:        image,
		ExposedPorts: []string{"9200/tcp"},
		Env:          env,
		WaitingFor: wait.ForHTTP("/_cluster/health").
			WithPort("9200/tcp").
			WithStartupTimeout(180 * time.Second),
		Started: true,
	})
	if err != nil {
		return "", nil, err
	}
	terminate := func() { _ = testcontainers.TerminateContainer(container) }
	host, err := container.Host(ctx)
	if err != nil {
		terminate()
		return "", nil, err
	}
	port, err := container.MappedPort(ctx, "9200")
	if err != nil {
		terminate()
		return "", nil, err
	}
	return fmt.Sprintf("%s://%s:%s/", scheme, host, port.Port()), terminate, nil
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

// osURL returns the base opensearch:// server URL for integration tests: the
// IQ_OPENSEARCH_URL override first, then the ephemeral container from TestMain. It is
// empty when no OpenSearch backend is available, so a test can skip cleanly.
func osURL() string {
	if u := os.Getenv("IQ_OPENSEARCH_URL"); u != "" {
		return u
	}
	return sharedOSURL
}

// skipShort skips an integration test under -short and returns a bounded context.
func skipShort(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping search integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// seedIndex seeds an index on the Elasticsearch backend (the default for the shared
// suite). It is seedIndexOn against testURL().
func seedIndex(t *testing.T, docs ...map[string]any) *Store {
	t.Helper()
	return seedIndexOn(t, testURL(), docs...)
}

// seedIndexOn creates a fresh index named after the test on the server at baseURL,
// indexes the supplied documents (each a map keyed by its "_id"), registers a drop on
// cleanup, and returns an index-scoped Store opened after the seed so it reads the
// populated mapping. It skips under -short. Passing no documents yields an empty index.
func seedIndexOn(t *testing.T, baseURL string, docs ...map[string]any) *Store {
	t.Helper()
	ctx := skipShort(t)
	name := indexName(t)

	// An admin store seeds and drops the index; it is opened before the index exists,
	// so it carries no mapping (which only the store under test needs).
	admin, err := Open(ctx, baseURL, name, nil, numfmt.DecimalAuto)
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
	st, err := Open(ctx, baseURL, name, nil, numfmt.DecimalAuto)
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

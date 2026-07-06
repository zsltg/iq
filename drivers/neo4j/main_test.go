package neo4j

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// neo4jImage is the pinned Neo4j image the integration tests run against, a generic
// container (a generic one adds no new module dependency, matching the CouchDB tests).
const neo4jImage = "neo4j:5"

// neo4jUser / neo4jPassword is the admin credential the container is configured with
// via NEO4J_AUTH (which also disables the forced first-run password change).
const neo4jUser, neo4jPassword = "neo4j", "password"

// sharedURL is the base neo4j:// server URL (no label) the integration tests connect
// to: an ephemeral Neo4j container started once for the whole package. It stays empty
// when integration tests are skipped (-short) or an external server is supplied
// (IQ_NEO4J_URL).
var sharedURL string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	base := os.Getenv("IQ_NEO4J_URL")
	if base == "" {
		ctx := context.Background()
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        neo4jImage,
				ExposedPorts: []string{"7687/tcp", "7474/tcp"},
				Env:          map[string]string{"NEO4J_AUTH": neo4jUser + "/" + neo4jPassword},
				WaitingFor:   wait.ForHTTP("/").WithPort("7474/tcp").WithStartupTimeout(180 * time.Second),
			},
			Started: true,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "start neo4j container: %v\n", err)
			return 1
		}
		defer func() { _ = testcontainers.TerminateContainer(container) }()
		host, err := container.Host(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "neo4j container host: %v\n", err)
			return 1
		}
		port, err := container.MappedPort(ctx, "7687")
		if err != nil {
			fmt.Fprintf(os.Stderr, "neo4j container port: %v\n", err)
			return 1
		}
		base = fmt.Sprintf("neo4j://%s:%s@%s:%s/", neo4jUser, neo4jPassword, host, port.Port())
	}
	sharedURL = base
	return m.Run()
}

// testURL returns the base neo4j:// server URL for integration tests: the
// IQ_NEO4J_URL override first, then the ephemeral container from TestMain, then a
// local default.
func testURL() string {
	if u := os.Getenv("IQ_NEO4J_URL"); u != "" {
		return u
	}
	if sharedURL != "" {
		return sharedURL
	}
	return fmt.Sprintf("neo4j://%s:%s@localhost:7687/", neo4jUser, neo4jPassword)
}

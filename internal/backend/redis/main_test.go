package redis_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// sharedURL is the Redis URL the integration tests connect to: an ephemeral
// container started once for the whole package. It stays empty when integration
// tests are skipped (-short) or an external server is supplied (IQ_REDIS_URL).
var sharedURL string

// TestMain provisions the test Redis before the suite runs. flag.Parse must run
// before testing.Short is read, and os.Exit skips deferred cleanup, so the work
// lives in runTests where the defer fires before the process exits.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	flag.Parse()
	// Only stand up a container when integration tests will actually run and no
	// external server was named; otherwise the default URL or the env override
	// stands in.
	if !testing.Short() && os.Getenv("IQ_REDIS_URL") == "" {
		ctx := context.Background()
		container, err := tcredis.Run(ctx, "redis:latest")
		if err != nil {
			fmt.Fprintf(os.Stderr, "start redis container: %v\n", err)
			return 1
		}
		defer func() { _ = testcontainers.TerminateContainer(container) }()

		url, err := container.ConnectionString(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "redis connection string: %v\n", err)
			return 1
		}
		sharedURL = url
	}
	return m.Run()
}

package mongo

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
)

// sharedURI is the MongoDB URI the integration tests connect to: an ephemeral
// container started once for the whole package. It stays empty when integration
// tests are skipped (-short) or an external server is supplied (IQ_MONGO_URL).
var sharedURI string

// TestMain provisions the test MongoDB before the suite runs. flag.Parse must run
// before testing.Short is read, and os.Exit skips deferred cleanup, so the work
// lives in runTests where the defer fires before the process exits.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	flag.Parse()
	// Only stand up a container when integration tests will actually run and no
	// external server was named; otherwise the default URI or the env override
	// stands in.
	if !testing.Short() && os.Getenv("IQ_MONGO_URL") == "" {
		ctx := context.Background()
		container, err := tcmongo.Run(ctx, "mongo:8")
		if err != nil {
			fmt.Fprintf(os.Stderr, "start mongodb container: %v\n", err)
			return 1
		}
		defer func() { _ = testcontainers.TerminateContainer(container) }()

		raw, err := container.ConnectionString(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mongodb connection string: %v\n", err)
			return 1
		}
		uri, err := withDatabase(raw, "iq_test")
		if err != nil {
			fmt.Fprintf(os.Stderr, "mongodb uri: %v\n", err)
			return 1
		}
		sharedURI = uri
	}
	return m.Run()
}

// withDatabase names the database in a mongodb URI. The query core requires the
// database in the URI path, but the container's connection string carries none.
func withDatabase(raw, db string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse mongodb uri: %w", err)
	}
	u.Path = "/" + db
	return u.String(), nil
}

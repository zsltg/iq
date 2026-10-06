package cmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/zsltg/iq/internal/testimage"
)

// TestMain provisions the Redis and MongoDB the cmd integration tests connect to:
// ephemeral containers started once for the package, their URLs exported as
// IQ_REDIS_URL / IQ_MONGO_URL so the tests' env fallbacks pick them up. flag.Parse
// must run before testing.Short is read, and os.Exit skips deferred cleanup, so
// the work lives in runTests where the defers fire before the process exits.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	flag.Parse()
	// `iq add` stores a password in the OS keyring by default. A test that does
	// not swap in a fake with useFakeKeyring gets a keyring that refuses every
	// write, so no test can write to the real keyring of the developer.
	keyringStore = &fakeKeyring{m: map[string]string{}, setErr: errors.New("no OS keyring in tests")}
	// Only stand up containers when integration tests will actually run and no
	// external server was named; otherwise the env override or default stands in.
	if !testing.Short() {
		ctx := context.Background()
		if os.Getenv("IQ_REDIS_URL") == "" {
			image, err := testimage.Ref("redis")
			if err != nil {
				fmt.Fprintf(os.Stderr, "redis image: %v\n", err)
				return 1
			}
			container, err := tcredis.Run(ctx, image)
			if err != nil {
				fmt.Fprintf(os.Stderr, "start redis container: %v\n", err)
				return 1
			}
			defer func() { _ = testcontainers.TerminateContainer(container) }()
			redisURL, err := container.ConnectionString(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "redis connection string: %v\n", err)
				return 1
			}
			_ = os.Setenv("IQ_REDIS_URL", redisURL)
		}
		if os.Getenv("IQ_MONGO_URL") == "" {
			image, err := testimage.Ref("mongo")
			if err != nil {
				fmt.Fprintf(os.Stderr, "mongodb image: %v\n", err)
				return 1
			}
			container, err := tcmongo.Run(ctx, image)
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
			mongoURL, err := withMongoDatabase(raw, "iq_test")
			if err != nil {
				fmt.Fprintf(os.Stderr, "mongodb uri: %v\n", err)
				return 1
			}
			_ = os.Setenv("IQ_MONGO_URL", mongoURL)
		}
	}
	return m.Run()
}

// withMongoDatabase names the database in a mongodb URI; the query core requires
// it in the path, but the container's connection string carries none.
func withMongoDatabase(raw, db string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse mongodb uri: %w", err)
	}
	u.Path = "/" + db
	return u.String(), nil
}

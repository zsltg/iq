package redis_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	iqredis "github.com/zsltg/iq/internal/redis"
)

// testURL returns the Redis URL for integration tests.
func testURL() string {
	if url := os.Getenv("IQ_REDIS_URL"); url != "" {
		return url
	}
	return "redis://localhost:6379/0"
}

func TestOpenRejectsBadURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := iqredis.Open(ctx, "://not-a-url")

	require.Error(t, err)
	require.ErrorContains(t, err, "parse redis url")
}

func TestOpenFailsFastWhenUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Port 1 has no listener, so the PING must fail fast.
	_, err := iqredis.Open(ctx, "redis://localhost:1")

	require.Error(t, err)
	require.ErrorContains(t, err, "connect redis")
}

func TestStoreRoundTrip(t *testing.T) {
	store := openIntegration(t)

	// SET returns the status string "OK" and verifies multi-arg forwarding.
	res, err := store.Query(context.Background(), []string{"SET", "iq:test:greeting", "hello"})
	require.NoError(t, err)
	require.Equal(t, "OK", res)

	got, err := store.Query(context.Background(), []string{"GET", "iq:test:greeting"})
	require.NoError(t, err)
	require.Equal(t, "hello", got)
}

func TestStoreMissingKeyIsNil(t *testing.T) {
	store := openIntegration(t)

	_, err := store.Query(context.Background(), []string{"DEL", "iq:test:absent"})
	require.NoError(t, err)

	got, err := store.Query(context.Background(), []string{"GET", "iq:test:absent"})
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestStoreReturnsCommandError(t *testing.T) {
	store := openIntegration(t)

	// GET with no key is a wrong-arity error from Redis.
	_, err := store.Query(context.Background(), []string{"GET"})

	require.Error(t, err)
	require.ErrorContains(t, err, "redis:")
}

// openIntegration skips under -short, otherwise opens a Store against the test
// Redis and registers its cleanup.
func openIntegration(t *testing.T) *iqredis.Store {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping redis integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	store, err := iqredis.Open(ctx, testURL())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

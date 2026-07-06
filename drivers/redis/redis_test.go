package redis_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	iqredis "github.com/zsltg/iq/drivers/redis"
	"github.com/zsltg/iq/internal/numfmt"
)

// testRedisDB is the reserved database integration tests operate on. DB 0 is
// left to developers, who seed it (scripts/seed-redis.sh) for manual exploration, so
// a test run never flushes data out from under them.
const testRedisDB = 15

// testURL returns the Redis URL for integration tests: IQ_REDIS_URL, which
// TestMain points at the reserved database on the ephemeral container or named
// server, then a local default on the same database when tests are skipped.
func testURL() string {
	if url := os.Getenv("IQ_REDIS_URL"); url != "" {
		return url
	}
	return "redis://localhost:6379/15"
}

// withRedisDB rewrites the database number in a redis URL's path.
func withRedisDB(raw string, db int) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse redis url: %w", err)
	}
	u.Path = "/" + strconv.Itoa(db)
	return u.String(), nil
}

func TestOpenRejectsBadURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := iqredis.Open(ctx, "://not-a-url", nil, numfmt.DecimalAuto)

	require.Error(t, err)
	require.ErrorContains(t, err, "parse redis url")
}

func TestOpenFailsFastWhenUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Port 1 has no listener, so the PING must fail fast.
	_, err := iqredis.Open(ctx, "redis://localhost:1", nil, numfmt.DecimalAuto)

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

func TestStoreUsesRESP2FlatReplies(t *testing.T) {
	store := openIntegration(t)

	_, err := store.Query(context.Background(), []string{"DEL", "iq:test:hash"})
	require.NoError(t, err)
	_, err = store.Query(context.Background(), []string{"HSET", "iq:test:hash", "f1", "v1", "f2", "v2"})
	require.NoError(t, err)

	got, err := store.Query(context.Background(), []string{"HGETALL", "iq:test:hash"})
	require.NoError(t, err)

	// RESP2 returns HGETALL as a flat array; RESP3 would return a map, so this
	// pins the Protocol=2 default set in Open.
	require.IsType(t, []any{}, got)
	require.ElementsMatch(t, []any{"f1", "v1", "f2", "v2"}, got)
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

	store, err := iqredis.Open(ctx, testURL(), nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

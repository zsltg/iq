package redis

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

// TestEstimateCountReflectsDBSize checks EstimateCount answers DBSIZE exactly and
// tracks the keyspace: five seeded keys read back as five, and a sixth as six,
// which pins the count against a hard-coded constant or an off-by-one. It runs on
// the private database (13) so its FLUSHDB never races other packages under
// `go test ./...`.
func TestEstimateCountReflectsDBSize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping redis integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := Open(ctx, redisURLOnDB(t, 13), nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	require.NoError(t, store.client.FlushDB(ctx).Err())
	for i := 0; i < 5; i++ {
		require.NoError(t, store.client.Set(ctx, fmt.Sprintf("iq:test:est:%d", i), "v", 0).Err())
	}

	n, err := store.EstimateCount(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(5), n, "estimate equals the seeded key count")

	require.NoError(t, store.client.Set(ctx, "iq:test:est:extra", "v", 0).Err())
	n, err = store.EstimateCount(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(6), n, "estimate tracks a newly added key")
}

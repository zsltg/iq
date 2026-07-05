package redis

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestScanBatchesBoundsPageSize checks the memory-bounding heart of ScanBatches:
// keys accumulate into a page and flush at exactly pageSize. It runs in the redis
// package (not redis_test) so it can lower the unexported pageSize, and flushes
// its own database first so the batch sizes are deterministic regardless of what
// other tests left behind.
func TestScanBatchesBoundsPageSize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping redis integration test in -short mode")
	}
	// TestMain pins IQ_REDIS_URL to a reserved database; the default matches it so
	// a stray run never flushes DB 0, which developers seed for manual exploration.
	url := os.Getenv("IQ_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/15"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := Open(ctx, url)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	require.NoError(t, store.client.FlushDB(ctx).Err())
	const n = 5
	for i := 0; i < n; i++ {
		require.NoError(t, store.client.Set(ctx, fmt.Sprintf("iq:test:page:%d", i), "v", 0).Err())
	}
	store.pageSize = 2

	var sizes []int
	err = store.ScanBatches(ctx, func(batch map[string]any) error {
		sizes = append(sizes, len(batch))
		return nil
	})
	require.NoError(t, err)

	total, maxSize := 0, 0
	for _, s := range sizes {
		total += s
		if s > maxSize {
			maxSize = s
		}
	}
	require.Equal(t, n, total, "every key delivered once on a stable keyspace")
	require.LessOrEqual(t, maxSize, store.pageSize, "no page exceeds pageSize")
	require.Contains(t, sizes, store.pageSize, "a page flushes at exactly pageSize")
}

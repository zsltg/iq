package redis

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
)

// TestScanFilteredSkipsFullyDroppedPage pins the empty-page guard: when every
// RedisJSON key on a page is a provable non-match, ScanFiltered advances the cursor
// without ever handing fn an empty batch. It runs in the redis package so it can
// lower the unexported pageSize, and flushes its reserved database first so the
// keyspace is exactly the fully-dropped docs.
func TestScanFilteredSkipsFullyDroppedPage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping redis integration test in -short mode")
	}
	url := os.Getenv("IQ_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/15"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := Open(ctx, url, nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	require.NoError(t, store.client.FlushDB(ctx).Err())
	const n = 3
	for i := range n {
		key := fmt.Sprintf("iq:test:drop:%d", i)
		require.NoError(t, store.client.Do(ctx, "JSON.SET", key, "$", fmt.Sprintf(`{"a":%d}`, i)).Err())
	}
	store.pageSize = 2

	// No document has a == 999, so every page is fully dropped and no batch fires.
	pred := predicate.Eq{Path: []string{"a"}, Value: 999.0}
	batches := 0
	err = store.ScanFiltered(ctx, pred, func(batch map[string]any) error {
		batches++
		require.NotEmpty(t, batch, "a fully-dropped page must not deliver an empty batch")
		return nil
	})
	require.NoError(t, err)
	require.Zero(t, batches, "every page was fully prefiltered, so fn is never called")
}

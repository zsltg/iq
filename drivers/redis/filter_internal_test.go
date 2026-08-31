package redis

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/rawpred"
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

// TestGetFilteredFailsFastOnTypeError pins that a filtered page aborts when the
// TYPE pipeline fails: without its own type for every key the read cannot choose a
// reader, so it must surface the failure rather than read on with none.
func TestGetFilteredFailsFastOnTypeError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	dead, cancelDead := context.WithCancel(ctx)
	cancelDead()

	_, err := store.getFiltered(dead, []string{"iq:test:filtertype"}, rawpred.NewMatcher(nil))

	require.ErrorContains(t, err, "redis type")
	require.ErrorIs(t, err, context.Canceled, "the pipeline's cause survives the wrap")
}

// TestGetFilteredToleratesAVanishedKey stages the race the filtered read path's
// redis.Nil tolerance exists for: a key TYPE saw is deleted before the value
// pipeline runs, so the pipeline itself reports redis.Nil. That is one key's
// absence, not a transport failure, and the page must still be delivered.
func TestGetFilteredToleratesAVanishedKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	const key = "iq:test:vanish"
	const survivor = "iq:test:survivor"
	require.NoError(t, store.client.MSet(ctx, key, "gone", survivor, "here").Err())
	// The value pipeline is the one whose first command is a GET; delete the key
	// just before it executes, exactly as a concurrent writer would.
	store.client.AddHook(beforePipelineHook{before: func(cmds []goredis.Cmder) {
		if len(cmds) > 0 && cmds[0].Name() == "get" {
			require.NoError(t, store.client.Del(ctx, key).Err())
		}
	}})

	got, err := store.getFiltered(ctx, []string{key, survivor}, rawpred.NewMatcher(nil))

	require.NoError(t, err, "a key that vanished mid-read is absence, not a read failure")
	require.NotContains(t, got, key)
	require.Equal(t, "here", got[survivor], "the rest of the page still arrives")
}

// TestGetFilteredNamesTheKeyThatFailedToRead pins the other half of that tolerance:
// a genuine per-key read failure (here a key retyped mid-read, so its GET answers
// WRONGTYPE) is reported and names the key, even when an earlier key's redis.Nil
// was what the pipeline returned.
func TestGetFilteredNamesTheKeyThatFailedToRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	const gone = "iq:test:gone"
	const retyped = "iq:test:retyped"
	require.NoError(t, store.client.MSet(ctx, gone, "a", retyped, "b").Err())
	store.client.AddHook(beforePipelineHook{before: func(cmds []goredis.Cmder) {
		if len(cmds) == 0 || cmds[0].Name() != "get" {
			return
		}
		// The first key vanishes (redis.Nil, tolerated) and the second becomes a
		// list, so its queued GET fails with WRONGTYPE.
		require.NoError(t, store.client.Del(ctx, gone, retyped).Err())
		require.NoError(t, store.client.RPush(ctx, retyped, "x").Err())
	}})

	_, err := store.getFiltered(ctx, []string{gone, retyped}, rawpred.NewMatcher(nil))

	require.ErrorContains(t, err, `read key "`+retyped+`"`)
	var redisErr goredis.Error
	require.ErrorAs(t, err, &redisErr, "the server's cause survives the wrap")
	require.ErrorContains(t, err, "WRONGTYPE")
}

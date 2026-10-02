package redis

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

// scanTestDB is the private database the keyspace-wide read tests below own. They
// flush it, so it must not be the reserved database the rest of the suite shares
// (15) nor the one the write tests flush (13).
const scanTestDB = 14

// openOnDB opens a Store against the private scan database and empties it, so a
// test that walks or counts the whole keyspace sees exactly the keys it seeded. It
// skips under -short like every other container-backed test here.
func openOnDB(t *testing.T, ctx context.Context, dec numfmt.DecimalMode) *Store { //nolint:revive // t first matches the other helpers in this package.
	t.Helper()
	if testing.Short() {
		t.Skip("skipping redis integration test in -short mode")
	}
	store, err := Open(ctx, redisURLOnDB(t, scanTestDB), nil, dec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.client.FlushDB(ctx).Err())
	return store
}

// seedStrings writes n string keys under prefix in one pipeline.
func seedStrings(t *testing.T, ctx context.Context, store *Store, prefix string, n int) { //nolint:revive // t first matches the other helpers in this package.
	t.Helper()
	p := store.client.Pipeline()
	for i := range n {
		p.Set(ctx, fmt.Sprintf("%s%d", prefix, i), "v", 0)
	}
	_, err := p.Exec(ctx)
	require.NoError(t, err)
}

// TestDedupe pins the collapse a page of SCAN keys relies on: a key repeated by a
// keyspace resize is read once, and first-seen order survives so a key lines up
// with its type in the parallel slices the read pipelines build.
func TestDedupe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "no duplicates keeps every key in order", in: []string{"a", "b", "c"}, want: []string{"a", "b", "c"}},
		{name: "a repeat collapses onto its first occurrence", in: []string{"a", "b", "a", "c", "b"}, want: []string{"a", "b", "c"}},
		{name: "every key the same collapses to one", in: []string{"a", "a", "a"}, want: []string{"a"}},
		{name: "no keys yields an empty slice", in: nil, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, dedupe(tt.in))
		})
	}
}

// TestScanBatchesWalksEveryCursorRound pins that the keyspace walk is a cursor
// loop, not a single round: with more keys than the SCAN COUNT hint, the iterator
// must fetch further rounds under the caller's context and still deliver every key
// exactly once.
func TestScanBatchesWalksEveryCursorRound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	const n = 3 * scanCount
	seedStrings(t, ctx, store, "iq:test:round:", n)

	seen := map[string]struct{}{}
	err := store.ScanBatches(ctx, func(batch map[string]any) error {
		for k := range batch {
			seen[k] = struct{}{}
		}
		return nil
	})

	require.NoError(t, err)
	require.Len(t, seen, n, "every key of every cursor round is delivered")
}

// TestScanBatchesStopsAtTheFirstPageError pins that a page failure aborts the walk
// rather than being swallowed and covered by a later page that succeeds.
func TestScanBatchesStopsAtTheFirstPageError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	seedStrings(t, ctx, store, "iq:test:pageerr:", 5)
	store.pageSize = 2

	sentinel := errors.New("stop")
	pages := 0
	err := store.ScanBatches(ctx, func(map[string]any) error {
		pages++
		if pages == 1 {
			return sentinel
		}
		return nil
	})

	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 1, pages, "the walk stops at the first failing page")
}

// TestScanBatchesSurfacesTheScanError pins the cursor's own failure: when SCAN
// itself fails the walk reports that error, rather than reading a truncated
// keyspace as a complete one.
func TestScanBatchesSurfacesTheScanError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	dead, cancelDead := context.WithCancel(ctx)
	cancelDead()

	err := store.ScanBatches(dead, func(map[string]any) error { return nil })

	require.ErrorContains(t, err, "redis scan")
	require.ErrorIs(t, err, context.Canceled, "the cursor's cause survives the wrap")
}

// TestGetHonorsDecimalMode pins that the store's decimal mode reaches the RedisJSON
// reader on the plain read path, the way ScanFiltered's reader already proves it on
// the filtered one: in string mode a fractional number stays its exact literal.
func TestGetHonorsDecimalMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalString)
	const key = "iq:test:decimal"
	require.NoError(t, store.client.Do(ctx, "JSON.SET", key, "$", `{"r":1.5}`).Err())

	got, err := store.Get(ctx, []string{key})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"r": "1.5"}, got[key], "string mode keeps the exact literal")
}

// beforePipelineHook runs before each pipeline executes, which is the only place a
// test can stage what happens between the driver's TYPE pipeline and its value
// pipeline: the mid-read race both read paths are written to tolerate.
type beforePipelineHook struct {
	before func(cmds []goredis.Cmder)
}

func (h beforePipelineHook) DialHook(next goredis.DialHook) goredis.DialHook { return next }

func (h beforePipelineHook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook { return next }

func (h beforePipelineHook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []goredis.Cmder) error {
		h.before(cmds)
		return next(ctx, cmds)
	}
}

// TestGetFailsFastOnTypeError proves Get stops when the TYPE pipeline fails. With
// no type for each key it cannot choose a reader.
func TestGetFailsFastOnTypeError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	dead, cancelDead := context.WithCancel(ctx)
	cancelDead()

	_, err := store.Get(dead, []string{"iq:test:gettype"})

	require.ErrorContains(t, err, "redis type")
	require.ErrorIs(t, err, context.Canceled, "the pipeline's cause survives the wrap")
}

// TestGetToleratesAVanishedKey stages the race on the plain read path: a key that
// TYPE saw is deleted before the value pipeline runs, so the pipeline returns
// redis.Nil. That is the absence of one key, and the other keys still arrive.
func TestGetToleratesAVanishedKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	const key = "iq:test:getvanish"
	const survivor = "iq:test:getsurvivor"
	require.NoError(t, store.client.MSet(ctx, key, "gone", survivor, "here").Err())
	store.client.AddHook(beforePipelineHook{before: func(cmds []goredis.Cmder) {
		if len(cmds) > 0 && cmds[0].Name() == "get" {
			require.NoError(t, store.client.Del(ctx, key).Err())
		}
	}})

	got, err := store.Get(ctx, []string{key, survivor})

	require.NoError(t, err, "a key that vanished mid-read is absence, not a read failure")
	require.Equal(t, map[string]any{survivor: "here"}, got)
}

// TestGetSurfacesAFailedValuePipeline proves Get reports a value pipeline that
// fails with a server error, and keeps that error in the chain. Here the key
// becomes a list after TYPE, so its GET answers WRONGTYPE.
func TestGetSurfacesAFailedValuePipeline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	const key = "iq:test:getretyped"
	require.NoError(t, store.client.Set(ctx, key, "a", 0).Err())
	store.client.AddHook(beforePipelineHook{before: func(cmds []goredis.Cmder) {
		if len(cmds) == 0 || cmds[0].Name() != "get" {
			return
		}
		require.NoError(t, store.client.Del(ctx, key).Err())
		require.NoError(t, store.client.RPush(ctx, key, "x").Err())
	}})

	_, err := store.Get(ctx, []string{key})

	require.ErrorContains(t, err, "redis read")
	var redisErr goredis.Error
	require.ErrorAs(t, err, &redisErr, "the server's cause survives the wrap")
	require.ErrorContains(t, err, "WRONGTYPE")
}

// TestGetNamesTheKeyThatFailedToRead proves Get reports a read failure of one key
// and names that key, also when the pipeline returned the redis.Nil of an
// earlier key.
func TestGetNamesTheKeyThatFailedToRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	const gone = "iq:test:getgone"
	const retyped = "iq:test:getretyped2"
	require.NoError(t, store.client.MSet(ctx, gone, "a", retyped, "b").Err())
	store.client.AddHook(beforePipelineHook{before: func(cmds []goredis.Cmder) {
		if len(cmds) == 0 || cmds[0].Name() != "get" {
			return
		}
		// The first key vanishes (redis.Nil, tolerated) and the second becomes a
		// list, so its GET fails with WRONGTYPE.
		require.NoError(t, store.client.Del(ctx, gone, retyped).Err())
		require.NoError(t, store.client.RPush(ctx, retyped, "x").Err())
	}})

	_, err := store.Get(ctx, []string{gone, retyped})

	require.ErrorContains(t, err, `read key "`+retyped+`"`)
	var redisErr goredis.Error
	require.ErrorAs(t, err, &redisErr, "the server's cause survives the wrap")
}

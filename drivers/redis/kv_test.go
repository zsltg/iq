package redis_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	iqredis "github.com/zsltg/iq/drivers/redis"
)

// seededKeys are the iq:test:* keys seedKV creates, one per supported type.
var seededKeys = []string{
	"iq:test:str", "iq:test:hash", "iq:test:list", "iq:test:set",
	"iq:test:zset", "iq:test:stream", "iq:test:json",
}

// seedKV loads a fixed set of iq:test:* keys spanning every supported type, so
// Get's normalization can be asserted without depending on scripts/seed.sh. It
// removes the keys when the test finishes: a stream or a leftover key would
// otherwise surface in a later `iq '.'` scan.
func seedKV(t *testing.T, store *iqredis.Store) {
	t.Helper()
	ctx := context.Background()
	cmds := [][]string{
		append([]string{"DEL"}, seededKeys...),
		{"SET", "iq:test:str", "39"},
		{"HSET", "iq:test:hash", "title", "Go", "year", "2015"},
		{"RPUSH", "iq:test:list", "a", "b", "c"},
		{"SADD", "iq:test:set", "gamma", "alpha", "beta"},
		{"ZADD", "iq:test:zset", "320", "one", "540", "two", "710", "three"},
		{"XADD", "iq:test:stream", "1-1", "sensor", "9", "temp", "18"},
		{"XADD", "iq:test:stream", "2-1", "temp", "19"},
		{"JSON.SET", "iq:test:json", "$", `{"a":1,"b":"x"}`},
	}
	for _, c := range cmds {
		_, err := store.Query(ctx, c)
		require.NoError(t, err)
	}
	t.Cleanup(func() { _, _ = store.Query(ctx, append([]string{"DEL"}, seededKeys...)) })
}

func TestGetNormalizesEveryType(t *testing.T) {
	store := openIntegration(t)
	seedKV(t, store)

	got, err := store.Get(context.Background(), append(seededKeys, "iq:test:absent"))
	require.NoError(t, err)

	require.Equal(t, "39", got["iq:test:str"], "numeric strings stay strings")
	require.Equal(t, map[string]any{"title": "Go", "year": "2015"}, got["iq:test:hash"])
	require.Equal(t, []any{"a", "b", "c"}, got["iq:test:list"], "list order preserved")
	require.Equal(t, []any{"alpha", "beta", "gamma"}, got["iq:test:set"], "set sorted lexically")
	require.Equal(t, []any{
		map[string]any{"member": "one", "score": float64(320)},
		map[string]any{"member": "two", "score": float64(540)},
		map[string]any{"member": "three", "score": float64(710)},
	}, got["iq:test:zset"], "zset in score-ascending {member,score} form")
	require.Equal(t, []any{
		map[string]any{"id": "1-1", "fields": map[string]any{"sensor": "9", "temp": "18"}},
		map[string]any{"id": "2-1", "fields": map[string]any{"temp": "19"}},
	}, got["iq:test:stream"], "stream as ordered [{id, fields}] entries")
	require.Equal(t, map[string]any{"a": float64(1), "b": "x"}, got["iq:test:json"], "RedisJSON parsed as JSON")
	require.Nil(t, got["iq:test:absent"], "missing key is null")
}

func TestGetEmptyKeysMakesNoRoundTrip(t *testing.T) {
	store := openIntegration(t)

	got, err := store.Get(context.Background(), nil)

	require.NoError(t, err)
	require.Equal(t, map[string]any{}, got)
}

func TestGetDeduplicatesKeys(t *testing.T) {
	store := openIntegration(t)
	seedKV(t, store)

	got, err := store.Get(context.Background(), []string{"iq:test:str", "iq:test:str"})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"iq:test:str": "39"}, got)
}

func TestGetRejectsUnsupportedType(t *testing.T) {
	store := openIntegration(t)
	ctx := context.Background()
	// A time-series key (TSDB-TYPE) has no frozen JSON encoding, so a named read
	// of it must fail fast rather than guess. Remove it so a later scan is clean.
	_, err := store.Query(ctx, []string{"DEL", "iq:test:ts"})
	require.NoError(t, err)
	_, err = store.Query(ctx, []string{"TS.ADD", "iq:test:ts", "*", "1"})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = store.Query(ctx, []string{"DEL", "iq:test:ts"}) })

	_, err = store.Get(ctx, []string{"iq:test:ts"})

	require.Error(t, err)
	require.ErrorContains(t, err, "unsupported redis type")
}

func TestScanBatchesYieldsSeededValues(t *testing.T) {
	store := openIntegration(t)
	seedKV(t, store)

	merged := map[string]any{}
	batches := 0
	err := store.ScanBatches(context.Background(), func(batch map[string]any) error {
		batches++
		for k, v := range batch {
			merged[k] = v
		}
		return nil
	})
	require.NoError(t, err)

	require.GreaterOrEqual(t, batches, 1, "at least one page was fed")
	// Every seeded key is present, already normalized: spot-check one shape.
	for _, k := range seededKeys {
		require.Contains(t, merged, k)
	}
	require.Equal(t, map[string]any{"title": "Go", "year": "2015"}, merged["iq:test:hash"])
}

func TestScanBatchesStopsOnCallbackError(t *testing.T) {
	store := openIntegration(t)
	seedKV(t, store)

	sentinel := errors.New("stop")
	err := store.ScanBatches(context.Background(), func(map[string]any) error {
		return sentinel
	})

	require.ErrorIs(t, err, sentinel)
}

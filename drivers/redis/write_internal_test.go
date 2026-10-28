package redis

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// TestTypedScanBoundsPageSize checks the memory bound of TypedScan: keys
// accumulate into a page and flush at exactly pageSize. It runs in the redis
// package so it can lower the unexported pageSize, mirroring
// TestScanBatchesBoundsPageSize. It flushes its own private database (13), not
// the reserved database that other tests share. So its FLUSHDB never races the
// cmd package under `go test ./...`.
func TestTypedScanBoundsPageSize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping redis integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := Open(ctx, redisURLOnDB(t, 13), nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	require.NoError(t, store.client.FlushDB(ctx).Err())
	const n = 5
	for i := range n {
		require.NoError(t, store.client.Set(ctx, fmt.Sprintf("iq:test:tscan:%d", i), "v", 0).Err())
	}
	store.pageSize = 2

	var sizes []int
	total, maxSize := 0, 0
	err = store.TypedScan(ctx, func(batch []query.Record) error {
		sizes = append(sizes, len(batch))
		total += len(batch)
		if len(batch) > maxSize {
			maxSize = len(batch)
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, n, total, "every key delivered once on a stable keyspace")
	require.LessOrEqual(t, maxSize, store.pageSize, "no page exceeds pageSize")
	require.Contains(t, sizes, store.pageSize, "a page flushes at exactly pageSize")
}

// TestClearEmptiesKeyspace checks that Clear (FLUSHDB) empties the keyspace. It
// runs on the private database (13) so its flush never races other packages.
func TestClearEmptiesKeyspace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping redis integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := Open(ctx, redisURLOnDB(t, 13), nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	require.NoError(t, store.client.Set(ctx, "iq:test:clear", "v", 0).Err())
	require.NoError(t, store.Clear(ctx))
	n, err := store.client.DBSize(ctx).Result()
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
}

// redisURLOnDB returns the integration Redis URL rewritten to database db, so a
// keyspace-scanning test can flush a private database. It falls back to the
// localhost default when IQ_REDIS_URL is unset.
func redisURLOnDB(t *testing.T, db int) string {
	t.Helper()
	raw := os.Getenv("IQ_REDIS_URL")
	if raw == "" {
		return fmt.Sprintf("redis://localhost:6379/%d", db)
	}
	u, err := url.Parse(raw)
	require.NoError(t, err)
	u.Path = fmt.Sprintf("/%d", db)
	return u.String()
}

func TestRedisString(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		want    string
		wantErr bool
	}{
		{name: "string", in: "hi", want: "hi"},
		{name: "int", in: 42, want: "42"},
		{name: "float", in: 3.5, want: "3.5"},
		{name: "bool", in: true, want: "true"},
		{name: "bigint", in: big.NewInt(9), want: "9"},
		{name: "nil", in: nil, want: ""},
		{name: "object rejected", in: map[string]any{"a": 1}, wantErr: true},
		{name: "array rejected", in: []any{1}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := redisString(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestResolveType(t *testing.T) {
	tests := []struct {
		name string
		rec  query.Record
		want string
	}{
		{name: "explicit tag wins", rec: query.Record{Type: "list", Value: []any{1}}, want: "list"},
		{name: "mongo document becomes json", rec: query.Record{Type: "document", Value: map[string]any{}}, want: "json"},
		{name: "untyped scalar is string", rec: query.Record{Value: "x"}, want: "string"},
		{name: "untyped object is json", rec: query.Record{Value: map[string]any{"a": 1}}, want: "json"},
		{name: "untyped array is json", rec: query.Record{Value: []any{1, 2}}, want: "json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, resolveType(tt.rec))
		})
	}
}

func TestAsFloat(t *testing.T) {
	f, err := asFloat(2)
	require.NoError(t, err)
	require.Equal(t, 2.0, f) //nolint:testifylint // exact equality intended: asFloat(2) must be exactly 2.0.
	f, err = asFloat("nope")
	require.Error(t, err)
	require.Zero(t, f, "a rejected score returns the zero value beside the error, never a sentinel")
}

func TestTypeName(t *testing.T) {
	require.Equal(t, "json", typeName("ReJSON-RL"))
	require.Equal(t, "hash", typeName("hash"))
}

func TestExplainWriteClearDrop(t *testing.T) {
	require.Contains(t, ExplainWrite(query.Upsert).Ops[0], "DEL then")
	require.Contains(t, ExplainWrite(query.InsertOnly).Ops[0], "EXISTS")
	require.Contains(t, ExplainClear().Ops[0], "FLUSHDB")
	plan, ok := ExplainDrop()
	require.False(t, ok)
	require.Contains(t, plan.Ops[0], "unsupported")
}

// TestQueueWriteClearsAnyExpiry pins that rewriting a string replaces the whole
// value, its time to live included. The contract of Put is replacement, so a
// rewritten key must not inherit the expiry of the value it replaced. Only
// queueWrite can show this. Put deletes an existing key before rewriting it, and
// that clears the expiry either way.
func TestQueueWriteClearsAnyExpiry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	const key = "iq:test:expiring"
	require.NoError(t, store.client.Set(ctx, key, "old", time.Hour).Err())

	_, err := store.client.Pipelined(ctx, func(p goredis.Pipeliner) error {
		return queueWrite(ctx, p, query.Record{Key: key, Type: "string", Value: "new"})
	})
	require.NoError(t, err)

	ttl, err := store.client.TTL(ctx, key).Result()
	require.NoError(t, err)
	require.Equal(t, time.Duration(-1), ttl, "the rewritten string carries no expiry")
	got, err := store.client.Get(ctx, key).Result()
	require.NoError(t, err)
	require.Equal(t, "new", got)
}

// TestDeleteAccumulatesAcrossChunks pins the chunked DEL accounting. Both counts
// are summed over every chunk, so a delete larger than one round-trip reports the
// whole batch and not only its last chunk.
func TestDeleteAccumulatesAcrossChunks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	require.NoError(t, store.client.MSet(ctx, "iq:test:d0", "a", "iq:test:d1", "b", "iq:test:d2", "c").Err())
	// Three chunks, each with a different present/missing split, so an assignment
	// in place of an accumulation cannot land on the right totals by luck.
	store.pageSize = 2

	stat, err := store.Delete(ctx, []string{
		"iq:test:d0", "iq:test:d1", "iq:test:d2", "iq:test:miss0", "iq:test:miss1",
	})

	require.NoError(t, err)
	require.Equal(t, 3, stat.Deleted, "every chunk's deletions are summed")
	require.Equal(t, 2, stat.Missing, "every chunk's misses are summed")
}

// TestWriteCommandsSurfaceTheCancelledContext pins that each command reports the
// failure of its call, with the cause kept in the chain, when its context is dead.
// Clear answers `iq data clear`, so reporting success on a keyspace that it did
// not empty is the worst possible lie. A failed DEL is reported with a zero count
// and is not read as a delete of keys that were missing. TypedScan reports a
// failed SCAN, so a cut keyspace is not taken for a complete one. The function
// typedGet stops when the TYPE pipeline fails, because with no type for each key
// it cannot choose a reader.
func TestWriteCommandsSurfaceTheCancelledContext(t *testing.T) {
	tests := []struct {
		name     string
		call     func(t *testing.T, ctx context.Context, s *Store) error
		fragment string
		cause    string
	}{
		{
			name: "clear reports the flush error",
			call: func(t *testing.T, ctx context.Context, s *Store) error {
				return s.Clear(ctx)
			},
			fragment: "redis flushdb",
			cause:    "the command's cause survives the wrap",
		},
		{
			name: "delete reports the del error with a zero count",
			call: func(t *testing.T, ctx context.Context, s *Store) error {
				stat, err := s.Delete(ctx, []string{"iq:test:k"})
				require.Zero(t, stat)
				return err
			},
			fragment: "redis del",
			cause:    "the command's cause survives the wrap",
		},
		{
			name: "typed scan reports the scan error",
			call: func(t *testing.T, ctx context.Context, s *Store) error {
				return s.TypedScan(ctx, func([]query.Record) error { return nil })
			},
			fragment: "redis scan",
			cause:    "the cursor's cause survives the wrap",
		},
		{
			name: "typed get fails fast on the type error",
			call: func(t *testing.T, ctx context.Context, s *Store) error {
				_, err := s.typedGet(ctx, []string{"iq:test:ttype"})
				return err
			},
			fragment: "redis type",
			cause:    "the pipeline's cause survives the wrap",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			store := openOnDB(t, ctx, numfmt.DecimalAuto)
			dead, cancelDead := context.WithCancel(ctx)
			cancelDead()

			err := tt.call(t, dead, store)

			require.ErrorContains(t, err, tt.fragment)
			require.ErrorIs(t, err, context.Canceled, tt.cause)
		})
	}
}

// TestQueueWriteRejectsANonScalarPart proves that queueWrite rejects a value
// whose parts do not have the shape of the type. It rejects the value before it
// queues a command, and it keeps the cause of the rejection in the chain. Each
// case puts an object where the type needs a scalar or a number.
func TestQueueWriteRejectsANonScalarPart(t *testing.T) {
	obj := map[string]any{"a": 1}
	tests := []struct {
		name    string
		record  query.Record
		wantErr string
	}{
		{"string value", query.Record{Key: "k", Type: "string", Value: obj}, `key "k": value map[string]interface {} is not a scalar`},
		{"hash field", query.Record{Key: "k", Type: "hash", Value: map[string]any{"f": obj}}, `key "k" field "f": value`},
		{"list element", query.Record{Key: "k", Type: "list", Value: []any{obj}}, `key "k": value`},
		{"set member", query.Record{Key: "k", Type: "set", Value: []any{obj}}, `key "k": value`},
		{"zset member", query.Record{Key: "k", Type: "zset", Value: []any{map[string]any{"member": obj, "score": 1.0}}}, `key "k": zset member: value`},
		{"zset score", query.Record{Key: "k", Type: "zset", Value: []any{map[string]any{"member": "m", "score": "high"}}}, `key "k": zset score: score string is not a number`},
		{"stream id", query.Record{Key: "k", Type: "stream", Value: []any{map[string]any{"id": obj, "fields": map[string]any{}}}}, `key "k": stream id: value`},
		{"stream field", query.Record{Key: "k", Type: "stream", Value: []any{map[string]any{"id": "1-0", "fields": map[string]any{"f": obj}}}}, `key "k" field "f": value`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPipeline(t)

			err := queueWrite(context.Background(), p, tt.record)

			require.ErrorContains(t, err, tt.wantErr)
			require.Error(t, errors.Unwrap(err), "the cause stays in the chain")
			require.Zero(t, p.Len(), "nothing is queued for a rejected record")
		})
	}
}

// TestPutInsertOnlyRejectsABadRecord proves the insert-only path stops at a
// record that queueWrite rejects, and writes nothing.
func TestPutInsertOnlyRejectsABadRecord(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)

	stat, err := store.Put(ctx, []query.Record{
		{Key: "iq:test:good", Type: "string", Value: "v"},
		{Key: "iq:test:bad", Type: "hash", Value: "not an object"},
	}, query.InsertOnly)

	require.ErrorContains(t, err, `key "iq:test:bad": hash value is not an object`)
	require.Zero(t, stat)
	n, err := store.client.Exists(ctx, "iq:test:good").Result()
	require.NoError(t, err)
	require.Zero(t, n, "the pipeline did not run")
}

// TestPutInsertOnlySurfacesAFailedWrite proves the insert-only path reports a
// write that the server refuses. XADD refuses the entry id 0-0.
func TestPutInsertOnlySurfacesAFailedWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)

	stat, err := store.Put(ctx, []query.Record{{
		Key:   "iq:test:badstream",
		Type:  "stream",
		Value: []any{map[string]any{"id": "0-0", "fields": map[string]any{"f": "v"}}},
	}}, query.InsertOnly)

	require.ErrorContains(t, err, "redis write")
	var redisErr goredis.Error
	require.ErrorAs(t, err, &redisErr, "the server's cause survives the wrap")
	require.Zero(t, stat)
}

// TestTypedScanWalksEveryCursorRound proves TypedScan is a cursor loop under the
// caller's context. With more keys than the SCAN COUNT hint, it must fetch more
// rounds and deliver every key once.
func TestTypedScanWalksEveryCursorRound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	const n = 3 * scanCount
	seedStrings(t, store, "iq:test:tround:", n)

	seen := map[string]struct{}{}
	err := store.TypedScan(ctx, func(batch []query.Record) error {
		for _, r := range batch {
			seen[r.Key] = struct{}{}
		}
		return nil
	})

	require.NoError(t, err)
	require.Len(t, seen, n, "every key of every cursor round is delivered")
}

// TestTypedScanSkipsAnEmptyKeyspace proves TypedScan never calls fn when the
// keyspace has no keys, so a caller never gets an empty batch.
func TestTypedScanSkipsAnEmptyKeyspace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)

	calls := 0
	err := store.TypedScan(ctx, func([]query.Record) error {
		calls++
		return nil
	})

	require.NoError(t, err)
	require.Zero(t, calls)
}

// TestTypedScanStopsAtTheFirstPageError proves a page failure stops the walk. A
// later page that succeeds must not hide it.
func TestTypedScanStopsAtTheFirstPageError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	seedStrings(t, store, "iq:test:tpageerr:", 5)
	store.pageSize = 2

	sentinel := errors.New("stop")
	pages := 0
	err := store.TypedScan(ctx, func([]query.Record) error {
		pages++
		if pages == 1 {
			return sentinel
		}
		return nil
	})

	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 1, pages, "the walk stops at the first failing page")
}

// TestTypedScanSurfacesAReadFailure proves TypedScan reports a page whose value
// pipeline fails. Here the key becomes a list after TYPE, so its GET answers
// WRONGTYPE.
func TestTypedScanSurfacesAReadFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)
	const key = "iq:test:tretyped"
	require.NoError(t, store.client.Set(ctx, key, "a", 0).Err())
	store.client.AddHook(beforePipelineHook{before: func(cmds []goredis.Cmder) {
		if len(cmds) == 0 || cmds[0].Name() != "get" {
			return
		}
		require.NoError(t, store.client.Del(ctx, key).Err())
		require.NoError(t, store.client.RPush(ctx, key, "x").Err())
	}})

	calls := 0
	err := store.TypedScan(ctx, func([]query.Record) error {
		calls++
		return nil
	})

	require.ErrorContains(t, err, "redis read")
	require.Zero(t, calls, "the failed page is not delivered")
}

func TestAsFloatInfinity(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		want    float64
		wantErr string
	}{
		{"inf", "inf", math.Inf(1), ""},
		{"plus inf", "+inf", math.Inf(1), ""},
		{"minus inf", "-inf", math.Inf(-1), ""},
		{"upper case", "INF", math.Inf(1), ""},
		{"nan text", "nan", 0, "score string is not a number or an infinity"},
		{"infinity word", "infinity", math.Inf(1), ""},
		{"minus infinity word", "-Infinity", math.Inf(-1), ""},
		{"overflow text", "1e999", 0, "score string is not a number or an infinity"},
		{"garbage text", "nope", 0, "score string is not a number or an infinity"},
		{"number text", "5", 0, "score string is not a number or an infinity"},
		{"nan float", math.NaN(), 0, "score is NaN"},
		{"inf float", math.Inf(1), math.Inf(1), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := asFloat(tt.in)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.InDelta(t, tt.want, f, 0)
			require.Equal(t, math.IsInf(tt.want, 0), math.IsInf(f, 0))
		})
	}
}

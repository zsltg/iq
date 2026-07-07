package redis

import (
	"context"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// TestTypedScanBoundsPageSize checks TypedScan's memory-bounding: keys accumulate
// into a page and flush at exactly pageSize. It runs in the redis package so it can
// lower the unexported pageSize, mirroring TestScanBatchesBoundsPageSize. It flushes
// its own private database (13) — not the reserved DB other tests share — so its
// FLUSHDB never races the cmd package under `go test ./...`.
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
	for i := 0; i < n; i++ {
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
	_, err = asFloat("nope")
	require.Error(t, err)
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

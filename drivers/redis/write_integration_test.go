package redis_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	iqredis "github.com/zsltg/iq/drivers/redis"
	"github.com/zsltg/iq/internal/query"
)

// freshKeys deletes the named keys before a test and again after it. So a write
// test isolates on its own keys. It does not flush the shared reserved database.
// A flush there races the tests of the cmd package under `go test ./...`.
func freshKeys(t *testing.T, store *iqredis.Store, keys ...string) {
	t.Helper()
	ctx := context.Background()
	del := append([]string{"DEL"}, keys...)
	_, err := store.Query(ctx, del)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = store.Query(ctx, del) })
}

// TestPutRoundTripIntegration asserts that normalize ∘ write is the identity for
// every native Redis type. A record that Put writes reads back to the same
// normalized value that the read path produced. This is what the per-record type
// tag buys. The type-ambiguous JSON of a hash and of a document reconstructs
// correctly.
func TestPutRoundTripIntegration(t *testing.T) {
	store := openIntegration(t)
	ctx := context.Background()
	freshKeys(t, store, "s", "h", "l", "set", "z", "st")

	records := []query.Record{
		{Key: "s", Type: "string", Value: "hello"},
		{Key: "h", Type: "hash", Value: map[string]any{"f1": "v1", "f2": "v2"}},
		{Key: "l", Type: "list", Value: []any{"x", "y", "z"}},
		{Key: "set", Type: "set", Value: []any{"a", "b", "c"}},
		{Key: "z", Type: "zset", Value: []any{
			map[string]any{"member": "a", "score": 1.0},
			map[string]any{"member": "b", "score": 2.0},
		}},
		{Key: "st", Type: "stream", Value: []any{
			map[string]any{"id": "1-1", "fields": map[string]any{"k": "v"}},
		}},
	}

	stat, err := store.Put(ctx, records, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, len(records), stat.Written)

	for _, want := range records {
		got, err := store.Get(ctx, []string{want.Key})
		require.NoError(t, err)
		require.Equal(t, want.Value, got[want.Key], "round-trip of %s key %q", want.Type, want.Key)
	}
}

func TestPutUpsertReplacesAggregateIntegration(t *testing.T) {
	store := openIntegration(t)
	ctx := context.Background()
	freshKeys(t, store, "l")

	_, err := store.Put(ctx, []query.Record{{Key: "l", Type: "list", Value: []any{"a", "b"}}}, query.Upsert)
	require.NoError(t, err)

	// Upsert must replace the whole list, not append to it.
	stat, err := store.Put(ctx, []query.Record{{Key: "l", Type: "list", Value: []any{"c"}}}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Overwritten)
	require.Equal(t, 0, stat.Written)

	got, err := store.Get(ctx, []string{"l"})
	require.NoError(t, err)
	require.Equal(t, []any{"c"}, got["l"])
}

func TestPutInsertOnlySkipsExistingIntegration(t *testing.T) {
	store := openIntegration(t)
	ctx := context.Background()
	freshKeys(t, store, "s", "t")

	_, err := store.Put(ctx, []query.Record{{Key: "s", Type: "string", Value: "orig"}}, query.Upsert)
	require.NoError(t, err)

	stat, err := store.Put(ctx, []query.Record{
		{Key: "s", Type: "string", Value: "new"},
		{Key: "t", Type: "string", Value: "fresh"},
	}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Written)
	require.Equal(t, 1, stat.Skipped)

	got, err := store.Get(ctx, []string{"s"})
	require.NoError(t, err)
	require.Equal(t, "orig", got["s"], "existing key must not be clobbered")
}

func TestPutRejectsUnencodableJSONIntegration(t *testing.T) {
	store := openIntegration(t)
	ctx := context.Background()
	freshKeys(t, store, "k")

	// A NaN has no JSON encoding, so a json-typed record fails fast at marshal time
	// rather than writing a corrupt value.
	_, err := store.Put(ctx, []query.Record{{Key: "k", Type: "json", Value: math.NaN()}}, query.Upsert)
	require.ErrorContains(t, err, "json")
	var unsupported *json.UnsupportedValueError
	require.ErrorAs(t, err, &unsupported, "the encoder's cause survives both wraps")
}

// TestPutRejectsAKeylessRecordIntegration pins the guard that stops a batch
// before it writes anything. A record with no key cannot be written under any
// mode, and the error carries the shared sentinel so the CLI can point at
// --key-field.
func TestPutRejectsAKeylessRecordIntegration(t *testing.T) {
	store := openIntegration(t)
	tests := []struct {
		name string
		mode query.WriteMode
	}{
		{name: "upsert", mode: query.Upsert},
		{name: "insert only", mode: query.InsertOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := store.Put(context.Background(), []query.Record{{Value: "x"}}, tt.mode)

			require.ErrorIs(t, err, query.ErrNoKey, "the sentinel survives the wrap")
			require.ErrorContains(t, err, "--key-field")
		})
	}
}

// TestPutRefusesAnEmptyAggregateIntegration pins the refusal of an empty value.
// Redis has no empty hash, list, set, sorted set or stream. An upsert of one
// would delete the old value and store nothing. So Put refuses the record, names
// the key and the type, and leaves the old value of the key alone.
func TestPutRefusesAnEmptyAggregateIntegration(t *testing.T) {
	store := openIntegration(t)
	ctx := context.Background()
	tests := []struct {
		name  string
		key   string
		kind  string
		value any
	}{
		{"hash", "eh", "hash", map[string]any{}},
		{"list", "el", "list", []any{}},
		{"set", "es", "set", []any{}},
		{"zset", "ez", "zset", []any{}},
		{"stream", "ex", "stream", []any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			freshKeys(t, store, tt.key)
			_, err := store.Put(ctx, []query.Record{{Key: tt.key, Type: "string", Value: "old"}}, query.Upsert)
			require.NoError(t, err)

			stat, err := store.Put(ctx, []query.Record{{Key: tt.key, Type: tt.kind, Value: tt.value}}, query.Upsert)

			require.ErrorContains(t, err, fmt.Sprintf("an empty %s cannot be stored", tt.kind))
			require.ErrorContains(t, err, fmt.Sprintf("key %q", tt.key))
			require.Zero(t, stat)
			got, err := store.Get(ctx, []string{tt.key})
			require.NoError(t, err)
			require.Equal(t, "old", got[tt.key], "the old value survives a refused write")
		})
	}
}

func TestDeleteIntegration(t *testing.T) {
	store := openIntegration(t)
	ctx := context.Background()
	freshKeys(t, store, "d1", "d2", "d3", "keep")

	_, err := store.Put(ctx, []query.Record{
		{Key: "d1", Type: "string", Value: "a"},
		{Key: "d2", Type: "string", Value: "b"},
		{Key: "keep", Type: "string", Value: "survivor"},
	}, query.Upsert)
	require.NoError(t, err)

	// Delete a mix: two present (d1, d2) and one already absent (d3). The DEL reply
	// gives an exact split.
	stat, err := store.Delete(ctx, []string{"d1", "d2", "d3"})
	require.NoError(t, err)
	require.Equal(t, 2, stat.Deleted)
	require.Equal(t, 1, stat.Missing)

	got, err := store.Get(ctx, []string{"d1", "d2", "keep"})
	require.NoError(t, err)
	require.Nil(t, got["d1"], "deleted key must be gone")
	require.Nil(t, got["d2"], "deleted key must be gone")
	require.Equal(t, "survivor", got["keep"], "a key not named must be untouched")
}

func TestDeleteEmptyIsNoOpIntegration(t *testing.T) {
	store := openIntegration(t)
	stat, err := store.Delete(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{}, stat)
}

func TestTypedScanIntegration(t *testing.T) {
	store := openIntegration(t)
	ctx := context.Background()
	freshKeys(t, store, "h", "s")

	_, err := store.Put(ctx, []query.Record{
		{Key: "h", Type: "hash", Value: map[string]any{"f": "v"}},
		{Key: "s", Type: "string", Value: "x"},
	}, query.Upsert)
	require.NoError(t, err)

	// Scan the whole keyspace but assert only the keys this test owns, so it does
	// not depend on the database being empty.
	got := map[string]query.Record{}
	require.NoError(t, store.TypedScan(ctx, func(batch []query.Record) error {
		for _, r := range batch {
			got[r.Key] = r
		}
		return nil
	}))
	require.Equal(t, "hash", got["h"].Type)
	require.Equal(t, map[string]any{"f": "v"}, got["h"].Value)
	require.Equal(t, "string", got["s"].Type)
}

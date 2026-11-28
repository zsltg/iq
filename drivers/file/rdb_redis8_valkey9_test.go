package file

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zsltg/rdb/core"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// The dumps in testdata/rdb come from real servers: redis:8.10.3 (RDB version 15)
// and valkey/valkey-bundle:9.1.4 (Valkey 9.1.3 with valkey-json, RDB version 80).
// cases/redis8_valkey9.sh in the branch feat/rdb-v13-v15-valkey of
// github.com/zsltg/rdb makes them again.

// openTestdataRDB opens a dump of testdata/rdb with content detection.
func openTestdataRDB(t *testing.T, name string) *Store {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", "rdb", name))
	require.NoError(t, err)
	st, err := Open(URL(path), numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	return st
}

// streamRecord builds the record value of a stream from id and field pairs.
func streamRecord(entries ...any) []any {
	out := make([]any, 0, len(entries)/2)
	for i := 0; i < len(entries); i += 2 {
		out = append(out, map[string]any{"id": entries[i], "fields": entries[i+1]})
	}
	return out
}

// commonDumpRecords are the records that both real dumps hold. The dumps write each
// type in each of its encodings, so these rows cover the decoders of both versions.
var commonDumpRecords = []query.Record{
	{Key: "str:plain", Type: "string", Value: "hello"},
	{Key: "str:int", Type: "string", Value: "12345"},
	{Key: "str:ttl", Type: "string", Value: "soon"},
	{Key: "list:small", Type: "list", Value: []any{"a", "b"}},
	{Key: "list:quick", Type: "list", Value: []any{"a", "b", "c", "d", "e"}},
	{Key: "set:intset", Type: "set", Value: []any{"1", "2"}},
	{Key: "set:listpack", Type: "set", Value: []any{"x", "y"}},
	{Key: "set:hash", Type: "set", Value: []any{"a", "b", "c", "d"}},
	{Key: "zset:listpack", Type: "zset", Value: []any{
		map[string]any{"member": "a", "score": float64(1)},
		map[string]any{"member": "b", "score": float64(2)},
	}},
	{Key: "zset:skiplist", Type: "zset", Value: []any{
		map[string]any{"member": "a", "score": float64(1)},
		map[string]any{"member": "d", "score": 1.5},
		map[string]any{"member": "b", "score": float64(2)},
		map[string]any{"member": "c", "score": float64(3)},
	}},
	{Key: "hash:listpack", Type: "hash", Value: map[string]any{"f1": "v1", "f2": "v2"}},
	{Key: "hash:table", Type: "hash", Value: map[string]any{"f1": "v1", "f2": "v2", "f3": "v3"}},
	// The two hashes with a field TTL. The record keeps the fields, not the TTL.
	{Key: "hash:lpttl", Type: "hash", Value: map[string]any{"f1": "v1", "f2": "v2"}},
	{Key: "hash:htttl", Type: "hash", Value: map[string]any{"f1": "v1", "f2": "v2", "f3": "v3"}},
	{Key: "stream:plain", Type: "stream", Value: streamRecord(
		"1-1", map[string]any{"a": "1"},
		"2-1", map[string]any{"b": "2"},
	)},
	// A stream with a consumer group and pending entries. The record has no groups.
	{Key: "stream:group", Type: "stream", Value: streamRecord(
		"1-1", map[string]any{"k": "v"},
		"2-1", map[string]any{"k": "w"},
	)},
}

func TestRDBRealDumps(t *testing.T) {
	redisOnly := []query.Record{
		// Redis 8.10 writes these hashes as template hashes (types 30 and 32).
		{Key: "tmpl:a", Type: "hash", Value: map[string]any{"name": "ann", "age": "30"}},
		{Key: "tmpl:b", Type: "hash", Value: map[string]any{"name": "bob", "age": "41"}},
	}
	tests := []struct {
		name string
		file string
		want []query.Record
	}{
		{name: "redis 8.10", file: "redis-8.10.rdb", want: append(append([]query.Record{}, commonDumpRecords...), redisOnly...)},
		{name: "valkey 9.1", file: "valkey-9.1.rdb", want: commonDumpRecords},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openTestdataRDB(t, tt.file)
			require.Equal(t, FormatRDB, st.format)

			recs := collect(t, st)
			for _, want := range tt.want {
				require.Equal(t, want, recs[want.Key], want.Key)
			}
			// The valkey-json and RedisJSON key is a module type, which the reader skips.
			require.NotContains(t, recs, "json:doc")
		})
	}
}

// TestRDBRedis810IdempotentStream reads a stream with IDMP data (Redis 8.6 and later).
// Its id is a time stamp, so the test checks the shape and the fields only.
func TestRDBRedis810IdempotentStream(t *testing.T) {
	recs := collect(t, openTestdataRDB(t, "redis-8.10.rdb"))
	rec, ok := recs["stream:idmp"]
	require.True(t, ok)
	require.Equal(t, "stream", rec.Type)
	msgs, ok := rec.Value.([]any)
	require.True(t, ok)
	require.Len(t, msgs, 1)
	require.Equal(t, map[string]any{"k": "v"}, msgs[0].(map[string]any)["fields"])
}

// TestRDBRealDumpKeyCount makes sure that a real dump yields one record per key
// that is not a module type, so no key is lost or doubled.
func TestRDBRealDumpKeyCount(t *testing.T) {
	tests := []struct {
		file string
		want int
	}{
		{file: "redis-8.10.rdb", want: len(commonDumpRecords) + 3}, // tmpl:a, tmpl:b, stream:idmp
		{file: "valkey-9.1.rdb", want: len(commonDumpRecords)},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			require.Len(t, collect(t, openTestdataRDB(t, tt.file)), tt.want)
		})
	}
}

func TestRDBUnsupportedTypeStopsRead(t *testing.T) {
	st := openTestdataRDB(t, "redis-8.10-array.rdb")
	err := st.TypedScan(context.Background(), func([]query.Record) error { return nil })
	require.ErrorIs(t, err, core.ErrUnsupportedType)
	require.EqualError(t, err, `parse rdb: unsupported object type: key "arr:a" has the array type (object type 28) `+
		`of Redis RDB version 15, which the decoder does not read`)
}

func TestDetectValkeyMagic(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "rdb", "valkey-9.1.rdb"))
	require.NoError(t, err)
	tests := []struct {
		name    string
		content []byte
		want    Format
	}{
		{name: "valkey 9 dump", content: data, want: FormatRDB},
		{name: "valkey header alone", content: []byte("VALKEY080"), want: FormatRDB},
		{name: "redis header alone", content: []byte("REDIS0015"), want: FormatRDB},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := DetectFormat(writeDump(t, "dump.bin", tt.content, ""))
			require.NoError(t, err)
			require.Equal(t, tt.want, f)
		})
	}
}

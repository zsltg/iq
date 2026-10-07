package redis

import (
	"context"
	"errors"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// TestQueueWrite pins what queueWrite queues for a record: the commands and
// their arguments for each type, the type it picks for an untyped value, nothing
// for an empty value, the exact error text of every shape error with nothing
// queued, and the partial queue of a stream whose later entry is bad. A list
// builds every value first, so a bad element queues nothing.
func TestQueueWrite(t *testing.T) {
	obj := map[string]any{"a": 1}
	entry := func(id string, fields any) map[string]any { return map[string]any{"id": id, "fields": fields} }
	tests := []struct {
		name    string
		record  query.Record
		want    []string
		wantErr string
	}{
		{"string", query.Record{Key: "k", Type: "string", Value: "v"}, []string{"set k v"}, ""},
		{"hash", query.Record{Key: "k", Type: "hash", Value: map[string]any{"f": "v"}}, []string{"hset k f v"}, ""},
		{"list", query.Record{Key: "k", Type: "list", Value: []any{"a", 1}}, []string{"rpush k a 1"}, ""},
		{"set", query.Record{Key: "k", Type: "set", Value: []any{"a", "b"}}, []string{"sadd k a b"}, ""},
		{
			"zset keeps the input order",
			query.Record{Key: "k", Type: "zset", Value: []any{
				map[string]any{"member": "m1", "score": 1.5},
				map[string]any{"member": "m2", "score": 2},
			}},
			[]string{"zadd k 1.5 m1 2 m2"},
			"",
		},
		{
			"stream keeps the entry order",
			query.Record{Key: "k", Type: "stream", Value: []any{entry("1-0", map[string]any{"f": "v"}), entry("2-0", map[string]any{"g": 2})}},
			[]string{"xadd k 1-0 f v", "xadd k 2-0 g 2"},
			"",
		},
		{"json", query.Record{Key: "k", Type: "json", Value: obj}, []string{`JSON.SET k $ {"a":1}`}, ""},
		{"untyped scalar is a string", query.Record{Key: "k", Value: "v"}, []string{"set k v"}, ""},
		{"untyped object is json", query.Record{Key: "k", Value: obj}, []string{`JSON.SET k $ {"a":1}`}, ""},
		{"untyped array is json", query.Record{Key: "k", Value: []any{1}}, []string{`JSON.SET k $ [1]`}, ""},
		{"document is json", query.Record{Key: "k", Type: "document", Value: obj}, []string{`JSON.SET k $ {"a":1}`}, ""},

		{"empty hash", query.Record{Key: "k", Type: "hash", Value: map[string]any{}}, []string{}, ""},
		{"empty list", query.Record{Key: "k", Type: "list", Value: []any{}}, []string{}, ""},
		{"empty set", query.Record{Key: "k", Type: "set", Value: []any{}}, []string{}, ""},
		{"empty zset", query.Record{Key: "k", Type: "zset", Value: []any{}}, []string{}, ""},
		{"empty stream", query.Record{Key: "k", Type: "stream", Value: []any{}}, []string{}, ""},

		{"hash not an object", query.Record{Key: "k", Type: "hash", Value: "x"}, []string{}, `key "k": hash value is not an object`},
		{"list not an array", query.Record{Key: "k", Type: "list", Value: "x"}, []string{}, `key "k": value is not an array`},
		{"set not an array", query.Record{Key: "k", Type: "set", Value: map[string]any{}}, []string{}, `key "k": value is not an array`},
		{"zset not an array", query.Record{Key: "k", Type: "zset", Value: "x"}, []string{}, `key "k": zset value is not an array`},
		{"zset element not an object", query.Record{Key: "k", Type: "zset", Value: []any{"x"}}, []string{}, `key "k": zset element is not a {member, score} object`},
		{"stream not an array", query.Record{Key: "k", Type: "stream", Value: "x"}, []string{}, `key "k": stream value is not an array`},
		{"stream entry not an object", query.Record{Key: "k", Type: "stream", Value: []any{"x"}}, []string{}, `key "k": stream entry is not an {id, fields} object`},
		{"stream fields not an object", query.Record{Key: "k", Type: "stream", Value: []any{entry("1-0", "x")}}, []string{}, `key "k": stream fields is not an object`},
		{"unsupported type", query.Record{Key: "k", Type: "TSDB-TYPE", Value: "x"}, []string{}, `key "k": unsupported redis type "TSDB-TYPE"`},
		{"json cannot be encoded", query.Record{Key: "k", Type: "json", Value: make(chan int)}, []string{}, `key "k": encode json: `},

		{
			"a bad later stream entry keeps the earlier ones queued",
			query.Record{Key: "k", Type: "stream", Value: []any{entry("1-0", map[string]any{"f": "v"}), entry("2-0", "x")}},
			[]string{"xadd k 1-0 f v"},
			`key "k": stream fields is not an object`,
		},
		{"a bad later list element queues nothing", query.Record{Key: "k", Type: "list", Value: []any{"a", obj}}, []string{}, `key "k": value`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
			t.Cleanup(func() { _ = client.Close() })
			p := client.Pipeline()

			err := queueWrite(context.Background(), p, tt.record)

			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
			require.Equal(t, tt.want, cmdLines(p))
		})
	}
}

// TestPutWrites pins the pipelines of both write modes. Upsert queues DEL then the
// write for each record, and the DEL reply decides Overwritten or Written.
// Insert-only reads EXISTS for every record, then writes the absent keys alone.
// Every pipeline carries the caller's context.
func TestPutWrites(t *testing.T) {
	batch := []query.Record{
		{Key: "a", Type: "string", Value: "1"},
		{Key: "b", Type: "string", Value: "2"},
	}
	tests := []struct {
		name      string
		mode      query.WriteMode
		ints      map[string]int64
		wantStat  query.WriteStat
		wantCalls [][]string
	}{
		{
			"upsert", query.Upsert,
			map[string]int64{"del a": 1, "del b": 0},
			query.WriteStat{Overwritten: 1, Written: 1},
			[][]string{{"del a", "set a 1", "del b", "set b 2"}},
		},
		{
			"insert-only", query.InsertOnly,
			map[string]int64{"exists a": 1, "exists b": 0},
			query.WriteStat{Skipped: 1, Written: 1},
			[][]string{{"exists a", "exists b"}, {"set b 2"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := markedContext()
			f := &fakeRedis{ints: tt.ints}
			store := newFakeStore(t, f, 10)

			stat, err := store.Put(ctx, batch, tt.mode)

			require.NoError(t, err)
			require.Equal(t, tt.wantStat, stat)
			calls := f.snapshot()
			require.Len(t, calls, len(tt.wantCalls))
			for i, c := range calls {
				require.Equal(t, tt.wantCalls[i], c.lines())
				require.Equal(t, ctx, c.ctx)
			}
		})
	}
}

// TestPutFailures pins each failure of both write modes. An empty key and a bad
// record stop before the pipeline that holds them runs, and the sentinel keeps
// its hint. A transport error is wrapped with the prefix of its pipeline. Every
// failure returns a zero stat. An empty batch is a no-op with no call.
func TestPutFailures(t *testing.T) {
	boom := errors.New("boom")
	good := query.Record{Key: "a", Type: "string", Value: "1"}
	noKey := query.Record{Key: "", Type: "string", Value: "2"}
	badHash := query.Record{Key: "b", Type: "hash", Value: "x"}
	hint := "; use --key-field for foreign input"
	tests := []struct {
		name      string
		mode      query.WriteMode
		batch     []query.Record
		fail      map[int]error
		wantText  string
		wantIs    error
		wantCalls int
	}{
		{"upsert empty key", query.Upsert, []query.Record{good, noKey}, nil, "redis write: ", query.ErrNoKey, 0},
		{"upsert empty key hint", query.Upsert, []query.Record{noKey}, nil, hint, query.ErrNoKey, 0},
		{"upsert bad record", query.Upsert, []query.Record{{Key: "a", Type: "hash", Value: "x"}}, nil, `key "a": hash value is not an object`, nil, 0},
		{"upsert transport error", query.Upsert, []query.Record{good}, map[int]error{0: boom}, "redis write: ", boom, 1},
		{"insert-only empty key", query.InsertOnly, []query.Record{good, noKey}, nil, "redis exists: ", query.ErrNoKey, 0},
		{"insert-only empty key hint", query.InsertOnly, []query.Record{noKey}, nil, hint, query.ErrNoKey, 0},
		{"insert-only bad record", query.InsertOnly, []query.Record{good, badHash}, nil, `key "b": hash value is not an object`, nil, 1},
		{"insert-only exists pipeline", query.InsertOnly, []query.Record{good}, map[int]error{0: boom}, "redis exists: ", boom, 1},
		{"insert-only write pipeline", query.InsertOnly, []query.Record{good}, map[int]error{1: boom}, "redis write: ", boom, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRedis{fail: tt.fail}
			store := newFakeStore(t, f, 10)

			stat, err := store.Put(context.Background(), tt.batch, tt.mode)

			require.ErrorContains(t, err, tt.wantText)
			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}
			require.Zero(t, stat)
			require.Len(t, f.snapshot(), tt.wantCalls)
		})
	}
	for _, mode := range []query.WriteMode{query.Upsert, query.InsertOnly} {
		t.Run("empty batch", func(t *testing.T) {
			f := &fakeRedis{}
			store := newFakeStore(t, f, 10)

			stat, err := store.Put(context.Background(), nil, mode)

			require.NoError(t, err)
			require.Zero(t, stat)
			require.Empty(t, f.snapshot())
		})
	}
}

// TestTypedScanPagesRecordsAndPinsScanCount pins TypedScan over the fake: pages of
// at most pageSize keys across SCAN rounds, the typed records of each page, the
// neutral json tag, a stored JSON null kept as a record, a vanished key skipped,
// fn called with an empty slice when a whole page vanished, and SCAN COUNT
// scanCount on every round.
func TestTypedScanPagesRecordsAndPinsScanCount(t *testing.T) {
	ctx := markedContext()
	f := &fakeRedis{
		keys: map[string]fakeKey{
			"a": {typ: "string", val: "v"},
			"b": {typ: "ReJSON-RL", val: "null"},
		},
		scan: [][]string{{"a", "b"}, {"gone"}},
	}
	store := newFakeStore(t, f, 2)

	var pages [][]query.Record
	err := store.TypedScan(ctx, func(batch []query.Record) error {
		pages = append(pages, batch)
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, [][]query.Record{
		{{Key: "a", Type: "string", Value: "v"}, {Key: "b", Type: "json", Value: nil}},
		{},
	}, pages)
	require.Equal(t, []string{"scan 0 match * count 100", "scan 1 match * count 100"}, scanLines(f))
	for _, c := range f.snapshot() {
		require.Equal(t, ctx, c.ctx)
	}
}

// TestTypedScanStopsAtAReadFailure pins that a failed TYPE pipeline ends the walk
// before fn runs.
func TestTypedScanStopsAtAReadFailure(t *testing.T) {
	boom := errors.New("boom")
	f := &fakeRedis{
		keys: map[string]fakeKey{"a": {typ: "string", val: "v"}},
		scan: [][]string{{"a"}},
		fail: map[int]error{1: boom},
	}
	store := newFakeStore(t, f, 2)
	pages := 0

	err := store.TypedScan(context.Background(), func([]query.Record) error {
		pages++
		return nil
	})

	require.ErrorContains(t, err, "redis type")
	require.ErrorIs(t, err, boom)
	require.Zero(t, pages)
}

package redis

import (
	"context"
	"errors"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// queuedBy runs queueWrite on a fresh pipeline and returns the commands it queued.
func queuedBy(t *testing.T, r query.Record) ([]string, error) {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	p := client.Pipeline()
	err := queueWrite(context.Background(), p, r)
	return cmdLines(p), err
}

// TestQueueWriteQueuesTheCommandsOfEachType pins the commands and the arguments
// that queueWrite queues for each type, and the type it picks for an untyped value.
func TestQueueWriteQueuesTheCommandsOfEachType(t *testing.T) {
	entries := []any{
		map[string]any{"id": "1-0", "fields": map[string]any{"f": "v"}},
		map[string]any{"id": "2-0", "fields": map[string]any{"g": 2}},
	}
	tests := []struct {
		name   string
		record query.Record
		want   []string
	}{
		{"string", query.Record{Key: "k", Type: "string", Value: "v"}, []string{"set k v"}},
		{"hash", query.Record{Key: "k", Type: "hash", Value: map[string]any{"f": "v"}}, []string{"hset k f v"}},
		{"list", query.Record{Key: "k", Type: "list", Value: []any{"a", 1}}, []string{"rpush k a 1"}},
		{"set", query.Record{Key: "k", Type: "set", Value: []any{"a", "b"}}, []string{"sadd k a b"}},
		{
			"zset keeps the input order",
			query.Record{Key: "k", Type: "zset", Value: []any{
				map[string]any{"member": "m1", "score": 1.5},
				map[string]any{"member": "m2", "score": 2},
			}},
			[]string{"zadd k 1.5 m1 2 m2"},
		},
		{"stream keeps the entry order", query.Record{Key: "k", Type: "stream", Value: entries}, []string{"xadd k 1-0 f v", "xadd k 2-0 g 2"}},
		{"json", query.Record{Key: "k", Type: "json", Value: map[string]any{"a": 1}}, []string{`JSON.SET k $ {"a":1}`}},
		{"untyped scalar is a string", query.Record{Key: "k", Value: "v"}, []string{"set k v"}},
		{"untyped object is json", query.Record{Key: "k", Value: map[string]any{"a": 1}}, []string{`JSON.SET k $ {"a":1}`}},
		{"untyped array is json", query.Record{Key: "k", Value: []any{1}}, []string{`JSON.SET k $ [1]`}},
		{"document is json", query.Record{Key: "k", Type: "document", Value: map[string]any{"a": 1}}, []string{`JSON.SET k $ {"a":1}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines, err := queuedBy(t, tt.record)

			require.NoError(t, err)
			require.Equal(t, tt.want, lines)
		})
	}
}

// TestQueueWriteRejectsAWrongShape pins the error text of every shape error, and
// that none of them queues a command.
func TestQueueWriteRejectsAWrongShape(t *testing.T) {
	tests := []struct {
		name    string
		record  query.Record
		wantErr string
	}{
		{"hash not an object", query.Record{Key: "k", Type: "hash", Value: "x"}, `key "k": hash value is not an object`},
		{"list not an array", query.Record{Key: "k", Type: "list", Value: "x"}, `key "k": value is not an array`},
		{"set not an array", query.Record{Key: "k", Type: "set", Value: map[string]any{}}, `key "k": value is not an array`},
		{"zset not an array", query.Record{Key: "k", Type: "zset", Value: "x"}, `key "k": zset value is not an array`},
		{"zset element not an object", query.Record{Key: "k", Type: "zset", Value: []any{"x"}}, `key "k": zset element is not a {member, score} object`},
		{"stream not an array", query.Record{Key: "k", Type: "stream", Value: "x"}, `key "k": stream value is not an array`},
		{"stream entry not an object", query.Record{Key: "k", Type: "stream", Value: []any{"x"}}, `key "k": stream entry is not an {id, fields} object`},
		{
			"stream fields not an object",
			query.Record{Key: "k", Type: "stream", Value: []any{map[string]any{"id": "1-0", "fields": "x"}}},
			`key "k": stream fields is not an object`,
		},
		{"unsupported type", query.Record{Key: "k", Type: "TSDB-TYPE", Value: "x"}, `key "k": unsupported redis type "TSDB-TYPE"`},
		{"json cannot be encoded", query.Record{Key: "k", Type: "json", Value: make(chan int)}, `key "k": encode json: `},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines, err := queuedBy(t, tt.record)

			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, lines)
		})
	}
}

// TestQueueWriteQueuesNothingForAnEmptyValue pins that Redis has no empty hash,
// list, set, sorted set or stream, so an empty value writes nothing and is not an
// error.
func TestQueueWriteQueuesNothingForAnEmptyValue(t *testing.T) {
	tests := []struct {
		name   string
		record query.Record
	}{
		{"hash", query.Record{Key: "k", Type: "hash", Value: map[string]any{}}},
		{"list", query.Record{Key: "k", Type: "list", Value: []any{}}},
		{"set", query.Record{Key: "k", Type: "set", Value: []any{}}},
		{"zset", query.Record{Key: "k", Type: "zset", Value: []any{}}},
		{"stream", query.Record{Key: "k", Type: "stream", Value: []any{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines, err := queuedBy(t, tt.record)

			require.NoError(t, err)
			require.Empty(t, lines)
		})
	}
}

// TestQueueWriteKeepsEarlierStreamEntriesWhenALaterOneFails pins the partial queue
// of a stream: entries before the bad one are already queued when the error
// returns. The caller never executes the pipeline after an error. A list builds
// every value first, so a bad element queues nothing.
func TestQueueWriteKeepsEarlierStreamEntriesWhenALaterOneFails(t *testing.T) {
	stream := query.Record{Key: "k", Type: "stream", Value: []any{
		map[string]any{"id": "1-0", "fields": map[string]any{"f": "v"}},
		map[string]any{"id": "2-0", "fields": "x"},
	}}
	list := query.Record{Key: "k", Type: "list", Value: []any{"a", map[string]any{}}}

	lines, err := queuedBy(t, stream)
	require.ErrorContains(t, err, `key "k": stream fields is not an object`)
	require.Equal(t, []string{"xadd k 1-0 f v"}, lines)

	lines, err = queuedBy(t, list)
	require.ErrorContains(t, err, `key "k": value`)
	require.Empty(t, lines)
}

// TestPutUpsertQueuesDelThenWritePerRecord pins the upsert pipeline: for each
// record in order, DEL then its write. The DEL reply decides Overwritten or Written.
func TestPutUpsertQueuesDelThenWritePerRecord(t *testing.T) {
	ctx := markedContext()
	f := &fakeRedis{ints: map[string]int64{"del a": 1, "del b": 0}}
	store := newFakeStore(t, f, 10)

	stat, err := store.Put(ctx, []query.Record{
		{Key: "a", Type: "string", Value: "1"},
		{Key: "b", Type: "string", Value: "2"},
	}, query.Upsert)

	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Overwritten: 1, Written: 1}, stat)
	calls := f.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, []string{"del a", "set a 1", "del b", "set b 2"}, calls[0].lines())
	require.Equal(t, ctx, calls[0].ctx)
}

// TestPutUpsertRejectsAnEmptyKeyBeforeAnyCall pins the empty-key error of an
// upsert: it wraps the sentinel, keeps the hint, and nothing reaches the server.
func TestPutUpsertRejectsAnEmptyKeyBeforeAnyCall(t *testing.T) {
	f := &fakeRedis{}
	store := newFakeStore(t, f, 10)

	stat, err := store.Put(context.Background(), []query.Record{
		{Key: "a", Type: "string", Value: "1"},
		{Key: "", Type: "string", Value: "2"},
	}, query.Upsert)

	require.ErrorIs(t, err, query.ErrNoKey)
	require.ErrorContains(t, err, "redis write: ")
	require.ErrorContains(t, err, "; use --key-field for foreign input")
	require.Zero(t, stat)
	require.Empty(t, f.snapshot())
}

// TestPutUpsertRejectsABadRecordAndSurfacesATransportError pins that a bad record
// stops the upsert before any call, and that a transport error is wrapped and
// returns a zero stat.
func TestPutUpsertRejectsABadRecordAndSurfacesATransportError(t *testing.T) {
	boom := errors.New("boom")

	f := &fakeRedis{}
	store := newFakeStore(t, f, 10)
	stat, err := store.Put(context.Background(), []query.Record{{Key: "a", Type: "hash", Value: "x"}}, query.Upsert)
	require.ErrorContains(t, err, `key "a": hash value is not an object`)
	require.Zero(t, stat)
	require.Empty(t, f.snapshot())

	f = &fakeRedis{fail: map[int]error{0: boom}}
	store = newFakeStore(t, f, 10)
	stat, err = store.Put(context.Background(), []query.Record{{Key: "a", Type: "string", Value: "1"}}, query.Upsert)
	require.ErrorContains(t, err, "redis write: ")
	require.ErrorIs(t, err, boom)
	require.Zero(t, stat)
}

// TestPutInsertOnlyWritesOnlyTheAbsentKeys pins the two pipelines of insert-only:
// EXISTS for every record, then the writes of the absent keys alone.
func TestPutInsertOnlyWritesOnlyTheAbsentKeys(t *testing.T) {
	ctx := markedContext()
	f := &fakeRedis{ints: map[string]int64{"exists a": 1, "exists b": 0}}
	store := newFakeStore(t, f, 10)

	stat, err := store.Put(ctx, []query.Record{
		{Key: "a", Type: "string", Value: "1"},
		{Key: "b", Type: "string", Value: "2"},
	}, query.InsertOnly)

	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Skipped: 1, Written: 1}, stat)
	calls := f.snapshot()
	require.Len(t, calls, 2)
	require.Equal(t, []string{"exists a", "exists b"}, calls[0].lines())
	require.Equal(t, []string{"set b 2"}, calls[1].lines())
	for _, c := range calls {
		require.Equal(t, ctx, c.ctx)
	}
}

// TestPutInsertOnlyFailures pins each failure of insert-only: an empty key stops
// before any call, a bad record stops before the write pipeline, and a transport
// error of either pipeline is wrapped with its own prefix. Every failure returns
// a zero stat.
func TestPutInsertOnlyFailures(t *testing.T) {
	boom := errors.New("boom")
	good := query.Record{Key: "a", Type: "string", Value: "1"}
	tests := []struct {
		name      string
		batch     []query.Record
		fail      map[int]error
		wantText  string
		wantIs    error
		wantCalls int
	}{
		{"empty key", []query.Record{good, {Key: "", Type: "string", Value: "2"}}, nil, "redis exists: ", query.ErrNoKey, 0},
		{"bad record", []query.Record{good, {Key: "b", Type: "hash", Value: "x"}}, nil, `key "b": hash value is not an object`, nil, 1},
		{"exists pipeline", []query.Record{good}, map[int]error{0: boom}, "redis exists: ", boom, 1},
		{"write pipeline", []query.Record{good}, map[int]error{1: boom}, "redis write: ", boom, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRedis{fail: tt.fail}
			store := newFakeStore(t, f, 10)

			stat, err := store.Put(context.Background(), tt.batch, query.InsertOnly)

			require.ErrorContains(t, err, tt.wantText)
			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}
			require.Zero(t, stat)
			require.Len(t, f.snapshot(), tt.wantCalls)
		})
	}
	// The sentinel's hint stays in the message.
	_, err := newFakeStore(t, &fakeRedis{}, 10).Put(context.Background(), []query.Record{{Type: "string", Value: "1"}}, query.InsertOnly)
	require.ErrorContains(t, err, "; use --key-field for foreign input")
}

// TestPutMakesNoCallForAnEmptyBatch documents that an empty batch is a no-op.
func TestPutMakesNoCallForAnEmptyBatch(t *testing.T) {
	f := &fakeRedis{}
	store := newFakeStore(t, f, 10)

	for _, mode := range []query.WriteMode{query.Upsert, query.InsertOnly} {
		stat, err := store.Put(context.Background(), nil, mode)

		require.NoError(t, err)
		require.Zero(t, stat)
	}
	require.Empty(t, f.snapshot())
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

// TestTypedScanStopsAtTheFirstError pins the stops of TypedScan: an fn error ends
// the walk, a read failure ends it, a SCAN error is wrapped and skips the final
// flush, and an empty keyspace calls nothing.
func TestTypedScanStopsAtTheFirstError(t *testing.T) {
	boom := errors.New("boom")
	keys := map[string]fakeKey{"a": {typ: "string", val: "v"}, "b": {typ: "string", val: "v"}, "c": {typ: "string", val: "v"}}
	tests := []struct {
		name      string
		scan      [][]string
		fail      map[int]error
		fnErr     error
		wantText  string
		wantIs    error
		wantPages int
	}{
		{"empty keyspace", nil, nil, nil, "", nil, 0},
		{"fn error", [][]string{{"a", "b", "c"}}, nil, boom, "", boom, 1},
		{"read failure", [][]string{{"a", "b", "c"}}, map[int]error{1: boom}, nil, "redis type", boom, 0},
		{"scan error", [][]string{{"a"}, {"b"}}, map[int]error{1: boom}, nil, "redis scan: ", boom, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRedis{keys: keys, scan: tt.scan, fail: tt.fail}
			store := newFakeStore(t, f, 2)
			pages := 0

			err := store.TypedScan(context.Background(), func([]query.Record) error {
				pages++
				return tt.fnErr
			})

			if tt.wantIs == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantIs)
			}
			if tt.wantText != "" {
				require.ErrorContains(t, err, tt.wantText)
			}
			require.Equal(t, tt.wantPages, pages)
		})
	}
}

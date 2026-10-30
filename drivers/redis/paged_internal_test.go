package redis

import (
	"context"
	"fmt"
	"math"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// element is one key with seven elements and the value that a read must give.
type element struct {
	name string
	key  fakeKey
	want any
}

// sevenElements returns one key of each type that has elements. The stored order
// is not the order of the output for a set and for a sorted set.
func sevenElements() []element {
	return []element{
		{
			"hash",
			fakeKey{typ: "hash", val: map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7"}},
			map[string]any{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7"},
		},
		{
			"list",
			fakeKey{typ: "list", val: []string{"d", "a", "g", "b", "f", "c", "a"}},
			[]any{"d", "a", "g", "b", "f", "c", "a"},
		},
		{
			"set",
			fakeKey{typ: "set", val: []string{"d", "a", "g", "b", "f", "c", "e"}},
			[]any{"a", "b", "c", "d", "e", "f", "g"},
		},
		{
			"zset",
			fakeKey{typ: "zset", val: []goredis.Z{
				{Member: "a", Score: 3},
				{Member: "b", Score: 1},
				{Member: "c", Score: 1},
				{Member: "d", Score: 2},
				{Member: "e", Score: 5},
				{Member: "f", Score: 4},
				{Member: "g", Score: 1},
			}},
			[]any{
				map[string]any{"member": "b", "score": 1.0},
				map[string]any{"member": "c", "score": 1.0},
				map[string]any{"member": "g", "score": 1.0},
				map[string]any{"member": "d", "score": 2.0},
				map[string]any{"member": "a", "score": 3.0},
				map[string]any{"member": "f", "score": 4.0},
				map[string]any{"member": "e", "score": 5.0},
			},
		},
		{
			"stream",
			fakeKey{typ: "stream", val: []goredis.XMessage{
				{ID: "1-0", Values: map[string]any{"f": "1"}},
				{ID: "1-1", Values: map[string]any{"f": "2"}},
				{ID: "2-0", Values: map[string]any{"f": "3"}},
				{ID: "3-0", Values: map[string]any{"f": "4"}},
				{ID: "3-1", Values: map[string]any{"f": "5"}},
				{ID: "4-0", Values: map[string]any{"f": "6"}},
				{ID: "5-0", Values: map[string]any{"f": "7"}},
			}},
			[]any{
				map[string]any{"id": "1-0", "fields": map[string]any{"f": "1"}},
				map[string]any{"id": "1-1", "fields": map[string]any{"f": "2"}},
				map[string]any{"id": "2-0", "fields": map[string]any{"f": "3"}},
				map[string]any{"id": "3-0", "fields": map[string]any{"f": "4"}},
				map[string]any{"id": "3-1", "fields": map[string]any{"f": "5"}},
				map[string]any{"id": "4-0", "fields": map[string]any{"f": "6"}},
				map[string]any{"id": "5-0", "fields": map[string]any{"f": "7"}},
			},
		},
	}
}

// limitedStore returns a fake store with the given limits.
func limitedStore(t *testing.T, key fakeKey, l limits) (*Store, *fakeRedis) {
	t.Helper()
	f := &fakeRedis{keys: map[string]fakeKey{"k": key}}
	store := newFakeStore(t, f, 10)
	store.limits = l
	return store, f
}

// TestKeyAboveTheLimitIsRefused pins the refusal of a key with more elements than
// the limit. The error names the key, the type, the size, and the remedy. It
// does not hold any value.
func TestKeyAboveTheLimitIsRefused(t *testing.T) {
	for _, tt := range sevenElements() {
		t.Run(tt.name, func(t *testing.T) {
			tt.key.per = 3
			store, _ := limitedStore(t, tt.key, limits{maxElements: 6, chunk: 3})

			got, err := store.Get(context.Background(), []string{"k"})

			require.ErrorIs(t, err, ErrKeyTooLarge)
			require.Nil(t, got)
			want := `read key "k": too many elements: the ` + tt.name + ` has 7 elements and the limit is 6, so read it with --unbounded`
			require.EqualError(t, err, want)
		})
	}
}

// TestKeyAtTheLimitIsRead pins that the limit is inclusive: a key with exactly
// the limit is read.
func TestKeyAtTheLimitIsRead(t *testing.T) {
	for _, tt := range sevenElements() {
		t.Run(tt.name, func(t *testing.T) {
			tt.key.per = 3
			store, _ := limitedStore(t, tt.key, limits{maxElements: 7, chunk: 3})

			got, err := store.Get(context.Background(), []string{"k"})

			require.NoError(t, err)
			require.Equal(t, map[string]any{"k": tt.want}, got)
		})
	}
}

// TestUnboundedReadsAKeyAboveTheLimit pins the opt-in. The same key that the
// limit refuses is read, in full, when the context is unbounded.
func TestUnboundedReadsAKeyAboveTheLimit(t *testing.T) {
	for _, tt := range sevenElements() {
		t.Run(tt.name, func(t *testing.T) {
			tt.key.per = 3
			store, _ := limitedStore(t, tt.key, limits{maxElements: 6, chunk: 3})

			got, err := store.Get(query.WithUnbounded(context.Background()), []string{"k"})

			require.NoError(t, err)
			require.Equal(t, map[string]any{"k": tt.want}, got)
		})
	}
}

// TestFilteredReadRefusesAKeyAboveTheLimit pins that the filtered read, which
// ScanFiltered uses, shares the limit.
func TestFilteredReadRefusesAKeyAboveTheLimit(t *testing.T) {
	key := sevenElements()[0].key
	store, _ := limitedStore(t, key, limits{maxElements: 6, chunk: 3})

	_, err := readPaths[1].read(store, context.Background(), []string{"k"})

	require.ErrorIs(t, err, ErrKeyTooLarge)
}

// TestPagedReadGivesTheValueOfAWholeRead pins that the value does not depend on
// the chunk size, on the order that the server returns elements in, or on a
// repeated element. A scan can return an element again. The fake does this for
// every round after the first.
func TestPagedReadGivesTheValueOfAWholeRead(t *testing.T) {
	chunks := []int64{1, 2, 3, 7, 1000}
	for _, tt := range sevenElements() {
		for _, chunk := range chunks {
			for _, repeat := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/chunk=%d/repeat=%t", tt.name, chunk, repeat), func(t *testing.T) {
					key := tt.key
					key.per, key.repeat = int(chunk), repeat
					store, _ := limitedStore(t, key, limits{maxElements: 100, chunk: chunk})

					got, err := store.Get(context.Background(), []string{"k"})

					require.NoError(t, err)
					require.Equal(t, map[string]any{"k": tt.want}, got)
				})
			}
		}
	}
}

// TestPagedReadUsesTheCursorCommands pins the commands of a key that needs more
// than one chunk. The first chunk rides in the pipeline with the size command.
// Each later chunk is one more command, and all of them carry the caller context.
func TestPagedReadUsesTheCursorCommands(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{"hash", []string{"hlen k", "hscan k 0 count 3", "hscan k 6 count 3", "hscan k 12 count 3"}},
		{"list", []string{"llen k", "lrange k 0 2", "lrange k 3 5", "lrange k 6 8"}},
		{"set", []string{"scard k", "sscan k 0 count 3", "sscan k 3 count 3", "sscan k 6 count 3"}},
		{"zset", []string{"zcard k", "zscan k 0 count 3", "zscan k 6 count 3", "zscan k 12 count 3"}},
		{"stream", []string{"xlen k", "xrange k - + count 3", "xrange k 2-1 + count 3", "xrange k 4-1 + count 3"}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := markedContext()
			key := sevenElements()[i].key
			key.per = 3
			store, f := limitedStore(t, key, limits{maxElements: 100, chunk: 3})

			_, err := store.Get(ctx, []string{"k"})

			require.NoError(t, err)
			calls := f.snapshot()
			require.Len(t, calls, 3+1, "one TYPE round trip, one for the first chunk, and one for each later chunk")
			require.Equal(t, []string{"type k"}, calls[0].lines())
			var got []string
			for _, c := range calls[1:] {
				got = append(got, c.lines()...)
				require.Equal(t, ctx, c.ctx)
			}
			require.Equal(t, tt.want, got)
		})
	}
}

// TestSizeCommandsRideInThePipeline pins the cost of a page of small keys. The
// size commands are in the value pipeline, so a page still takes two round trips.
func TestSizeCommandsRideInThePipeline(t *testing.T) {
	keys := map[string]fakeKey{"s": {typ: "string", val: "v"}}
	ask := []string{"s"}
	for _, tt := range sevenElements() {
		keys["k-"+tt.name] = fakeKey{typ: tt.key.typ, val: tt.key.val}
		ask = append(ask, "k-"+tt.name)
	}
	f := &fakeRedis{keys: keys}
	store := newFakeStore(t, f, 10)

	_, err := store.Get(context.Background(), ask)

	require.NoError(t, err)
	calls := f.snapshot()
	require.Len(t, calls, 2)
	require.Contains(t, calls[1].lines(), "hlen k-hash")
	require.Contains(t, calls[1].lines(), "llen k-list")
	require.Contains(t, calls[1].lines(), "scard k-set")
	require.Contains(t, calls[1].lines(), "zcard k-zset")
	require.Contains(t, calls[1].lines(), "xlen k-stream")
	require.NotContains(t, calls[1].lines(), "strlen s")
}

// TestPagedZsetReportsNonFiniteScores pins that a score that is not finite is
// reported once, at its index in the sorted result, when it arrives in a later
// chunk.
func TestPagedZsetReportsNonFiniteScores(t *testing.T) {
	type call struct{ key, field string }
	var got []call
	ctx := query.WithNonFiniteNotice(context.Background(), func(key, field string) {
		got = append(got, call{key, field})
	})
	key := fakeKey{typ: "zset", per: 1, val: []goredis.Z{
		{Member: "lo", Score: math.Inf(-1)}, {Member: "a", Score: 1}, {Member: "hi", Score: math.Inf(1)},
	}}
	store, _ := limitedStore(t, key, limits{maxElements: 100, chunk: 1})

	v, err := store.Get(ctx, []string{"k"})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"k": []any{
		map[string]any{"member": "lo", "score": "-inf"},
		map[string]any{"member": "a", "score": 1.0},
		map[string]any{"member": "hi", "score": "inf"},
	}}, v)
	require.Equal(t, []call{{"k", "[0].score"}, {"k", "[2].score"}}, got)
}

// TestNextStreamID pins the ID that follows a stream entry, and the refusal of a
// malformed ID.
func TestNextStreamID(t *testing.T) {
	tests := []struct {
		id, want, wantErr string
	}{
		{"1-0", "1-1", ""},
		{"1700000000000-41", "1700000000000-42", ""},
		{"5-18446744073709551615", "6-0", ""},
		{"5", "", `stream entry ID "5" is not <ms>-<seq>`},
		{"a-1", "", `stream entry ID "a-1" is not <ms>-<seq>`},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got, err := nextStreamID(tt.id)

			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestScanReplyWithAnOddPairCountIsAnError pins that a malformed hash or sorted
// set reply is an error and not a lost field.
func TestScanReplyWithAnOddPairCountIsAnError(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		reader reader
	}{
		{"hash", hashReader{onePage(ctx), scanReply(ctx, "a")}},
		{"zset", zsetReader{onePage(ctx), scanReply(ctx, "a")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, present, err := tt.reader.normalize()

			require.ErrorContains(t, err, "expected pairs")
			require.False(t, present)
		})
	}
}

// TestZsetReaderRefusesABadScore pins that a score that is not a number is an
// error.
func TestZsetReaderRefusesABadScore(t *testing.T) {
	ctx := context.Background()

	_, present, err := zsetReader{onePage(ctx), scanReply(ctx, "a", "x")}.normalize()

	require.ErrorContains(t, err, "parse score")
	require.False(t, present)
}

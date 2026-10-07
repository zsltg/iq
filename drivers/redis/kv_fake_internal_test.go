package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/rawpred"
)

// fakeCtxKey marks the contexts that the tests pass in, so a test sees which
// context reached the fake.
type fakeCtxKey struct{}

// markedContext returns a context that carries a marker no other context has.
func markedContext() context.Context {
	return context.WithValue(context.Background(), fakeCtxKey{}, "marked")
}

// lines renders each command of a call as one space-separated line.
func (c fakeCall) lines() []string {
	out := make([]string, len(c.cmds))
	for i, args := range c.cmds {
		parts := make([]string, len(args))
		for j, a := range args {
			parts[j] = fmt.Sprint(a)
		}
		out[i] = strings.Join(parts, " ")
	}
	return out
}

// cmdLines renders the commands that a pipeline has queued.
func cmdLines(p goredis.Pipeliner) []string {
	return fakeCall{cmds: argsOf(p.Cmds())}.lines()
}

// argsOf returns the arguments of each command.
func argsOf(cmds []goredis.Cmder) [][]any {
	out := make([][]any, len(cmds))
	for i, c := range cmds {
		out[i] = c.Args()
	}
	return out
}

// TestReaderForQueuesTheReadOfEachType pins the dispatch of readerFor: for each
// TYPE reply it queues the one value command that reads the type, and returns
// the reader that normalizes that reply.
func TestReaderForQueuesTheReadOfEachType(t *testing.T) {
	tests := []struct {
		typ        string
		wantReader reader
		wantLines  []string
	}{
		{"none", missingReader{}, []string{}},
		{"string", stringReader{}, []string{"get k"}},
		{"hash", hashReader{}, []string{"hgetall k"}},
		{"list", listReader{}, []string{"lrange k 0 -1"}},
		{"set", setReader{}, []string{"smembers k"}},
		{"zset", zsetReader{}, []string{"zrange k 0 -1 withscores"}},
		{"stream", streamReader{}, []string{"xrange k - +"}},
		{"ReJSON-RL", jsonReader{}, []string{"JSON.GET k"}},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
			t.Cleanup(func() { _ = client.Close() })
			p := client.Pipeline()

			r, err := readerFor(context.Background(), p, typedKey{key: "k", typ: tt.typ}, numfmt.DecimalString)

			require.NoError(t, err)
			require.IsType(t, tt.wantReader, r)
			require.Equal(t, tt.wantLines, cmdLines(p))
			if j, ok := r.(jsonReader); ok {
				require.Equal(t, numfmt.DecimalString, j.decimal, "the decimal mode reaches the reader")
				require.NotNil(t, j.cmd)
			}
		})
	}
}

// TestReaderForRefusesAnUnsupportedType pins the error for a module type with no
// frozen JSON encoding, and that it queues nothing.
func TestReaderForRefusesAnUnsupportedType(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	p := client.Pipeline()

	r, err := readerFor(context.Background(), p, typedKey{key: "k", typ: "TSDB-TYPE"}, numfmt.DecimalAuto)

	require.EqualError(t, err, `unsupported redis type "TSDB-TYPE" for key "k"`)
	require.Nil(t, r)
	require.Zero(t, p.Len())
}

// TestGetReadsTypesThenValuesInKeyOrder pins the two pipelines of Get: TYPE for
// each unique key in first-seen order, then each key's value command in the same
// order. Both carry the caller's context.
func TestGetReadsTypesThenValuesInKeyOrder(t *testing.T) {
	ctx := markedContext()
	f := &fakeRedis{keys: map[string]fakeKey{
		"b": {typ: "string", val: "vb"},
		"a": {typ: "hash", val: map[string]string{"f": "v"}},
	}}
	store := newFakeStore(t, f, 10)

	got, err := store.Get(ctx, []string{"b", "a", "b", "gone"})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"b": "vb", "a": map[string]any{"f": "v"}}, got)
	calls := f.snapshot()
	require.Len(t, calls, 2)
	require.Equal(t, []string{"type b", "type a", "type gone"}, calls[0].lines())
	require.Equal(t, []string{"get b", "hgetall a"}, calls[1].lines())
	for _, c := range calls {
		require.True(t, c.piped)
		require.Equal(t, ctx, c.ctx, "the caller's context reaches the pipeline")
	}
}

// TestGetTreatsAPerCommandNilAsAbsent pins that a value command that answers Nil
// (the key vanished after TYPE) drops that key and keeps the rest of the page.
func TestGetTreatsAPerCommandNilAsAbsent(t *testing.T) {
	f := &fakeRedis{keys: map[string]fakeKey{
		"gone": {typ: "string", err: goredis.Nil},
		"here": {typ: "string", val: "v"},
	}}
	store := newFakeStore(t, f, 10)

	got, err := store.Get(context.Background(), []string{"gone", "here"})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"here": "v"}, got)
}

// TestGetSurfacesAPipelineTransportError pins the wrap of a failed pipeline. A
// failed TYPE pipeline stops Get before the value pipeline.
func TestGetSurfacesAPipelineTransportError(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name      string
		failAt    int
		wantText  string
		wantCalls int
	}{
		{"type pipeline", 0, "redis type", 1},
		{"value pipeline", 1, "redis read", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRedis{
				keys: map[string]fakeKey{"k": {typ: "string", val: "v"}},
				fail: map[int]error{tt.failAt: boom},
			}
			store := newFakeStore(t, f, 10)

			got, err := store.Get(context.Background(), []string{"k"})

			require.ErrorContains(t, err, tt.wantText)
			require.ErrorIs(t, err, boom)
			require.Nil(t, got)
			require.Len(t, f.snapshot(), tt.wantCalls)
		})
	}
}

// TestGetNamesTheKeyOfAReaderError pins that a reader error names its key.
func TestGetNamesTheKeyOfAReaderError(t *testing.T) {
	boom := errors.New("wrongtype")
	// A pipeline reports its first failed command, so the Nil of the first key
	// is what Pipelined returns, and the reader of the second key must name itself.
	f := &fakeRedis{keys: map[string]fakeKey{
		"gone": {typ: "string", err: goredis.Nil},
		"bad":  {typ: "hash", err: boom},
	}}
	store := newFakeStore(t, f, 10)

	_, err := store.Get(context.Background(), []string{"gone", "bad"})

	require.ErrorContains(t, err, `read key "bad"`)
	require.ErrorIs(t, err, boom)
}

// TestGetFilteredReadsTypesThenValuesInKeyOrder pins the command order of the
// filtered read: TYPE for each unique key, then one value command each, with
// JSON.GET for a RedisJSON key. Both pipelines carry the caller's context.
func TestGetFilteredReadsTypesThenValuesInKeyOrder(t *testing.T) {
	ctx := markedContext()
	f := &fakeRedis{keys: map[string]fakeKey{
		"s": {typ: "string", val: "v"},
		"j": {typ: "ReJSON-RL", val: `{"a":1}`},
	}}
	store := newFakeStore(t, f, 10)

	got, err := store.getFiltered(ctx, []string{"s", "j", "s"}, rawpred.NewMatcher(nil))

	require.NoError(t, err)
	require.Equal(t, map[string]any{"s": "v", "j": map[string]any{"a": 1}}, got)
	calls := f.snapshot()
	require.Len(t, calls, 2)
	require.Equal(t, []string{"type s", "type j"}, calls[0].lines())
	require.Equal(t, []string{"get s", "JSON.GET j"}, calls[1].lines())
	for _, c := range calls {
		require.Equal(t, ctx, c.ctx)
	}
}

// TestGetFilteredWrapsEachFailure pins the wraps of the filtered read: a failed
// TYPE pipeline, a failed value pipeline, a reader error that names its key, and
// an unsupported type that stops before the value pipeline runs.
func TestGetFilteredWrapsEachFailure(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name     string
		keys     map[string]fakeKey
		fail     map[int]error
		wantText string
	}{
		{"type pipeline", map[string]fakeKey{"k": {typ: "string"}}, map[int]error{0: boom}, "redis type"},
		{"value pipeline", map[string]fakeKey{"k": {typ: "string"}}, map[int]error{1: boom}, "redis read"},
		{"reader error", map[string]fakeKey{"gone": {typ: "string", err: goredis.Nil}, "k": {typ: "hash", err: boom}}, nil, `read key "k"`},
		{"unsupported type", map[string]fakeKey{"k": {typ: "TSDB-TYPE"}}, nil, `unsupported redis type "TSDB-TYPE" for key "k"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRedis{keys: tt.keys, fail: tt.fail}
			store := newFakeStore(t, f, 10)

			_, err := store.getFiltered(context.Background(), []string{"gone", "k"}, rawpred.NewMatcher(nil))

			require.ErrorContains(t, err, tt.wantText)
			if tt.fail != nil || tt.name == "reader error" {
				require.ErrorIs(t, err, boom)
			}
		})
	}
}

// TestGetFilteredTreatsAPerCommandNilAsAbsent pins that a key that vanished after
// TYPE is absent from the filtered read, and the rest of the page arrives.
func TestGetFilteredTreatsAPerCommandNilAsAbsent(t *testing.T) {
	f := &fakeRedis{keys: map[string]fakeKey{
		"gone": {typ: "ReJSON-RL", err: goredis.Nil},
		"here": {typ: "string", val: "v"},
	}}
	store := newFakeStore(t, f, 10)

	got, err := store.getFiltered(context.Background(), []string{"gone", "here"}, rawpred.NewMatcher(nil))

	require.NoError(t, err)
	require.Equal(t, map[string]any{"here": "v"}, got)
}

// TestListAndSetReadersOrderAndErrors pins the two string-slice readers: a list
// keeps its order, a set is sorted, and both return the error of the reply.
func TestListAndSetReadersOrderAndErrors(t *testing.T) {
	ctx := context.Background()
	slice := func(vs []string, err error) *goredis.StringSliceCmd {
		c := goredis.NewStringSliceCmd(ctx)
		c.SetVal(vs)
		c.SetErr(err)
		return c
	}
	boom := errors.New("boom")

	v, present, err := listReader{slice([]string{"c", "a", "b"}, nil)}.normalize()
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, []any{"c", "a", "b"}, v, "a list keeps its order")

	v, present, err = setReader{slice([]string{"c", "a", "b"}, nil)}.normalize()
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, []any{"a", "b", "c"}, v, "a set is sorted")

	for name, r := range map[string]reader{
		"list": listReader{slice([]string{"a"}, boom)},
		"set":  setReader{slice([]string{"a"}, boom)},
	} {
		t.Run(name+" error", func(t *testing.T) {
			v, present, err := r.normalize()

			require.ErrorIs(t, err, boom)
			require.False(t, present)
			require.Nil(t, v)
		})
	}
}

// scanLines returns the SCAN commands that the fake recorded, one line each.
func scanLines(f *fakeRedis) []string {
	var out []string
	for _, c := range f.snapshot() {
		for _, l := range c.lines() {
			if strings.HasPrefix(l, "scan ") {
				out = append(out, l)
			}
		}
	}
	return out
}

// TestScanBatchesSplitsPagesAndPinsScanCount pins the cursor walk of ScanBatches:
// pages hold at most pageSize keys across SCAN rounds, the last page is partial,
// and every SCAN carries COUNT scanCount (not pageSize) and the caller's context.
func TestScanBatchesSplitsPagesAndPinsScanCount(t *testing.T) {
	ctx := markedContext()
	keys := map[string]fakeKey{}
	for _, k := range []string{"k1", "k2", "k3", "k4", "k5"} {
		keys[k] = fakeKey{typ: "string", val: k}
	}
	f := &fakeRedis{keys: keys, scan: [][]string{{"k1", "k2", "k3"}, {"k4", "k5"}}}
	store := newFakeStore(t, f, 2)

	var pages [][]string
	err := store.ScanBatches(ctx, func(batch map[string]any) error {
		var ks []string
		for k := range batch {
			ks = append(ks, k)
		}
		pages = append(pages, ks)
		return nil
	})

	require.NoError(t, err)
	require.Len(t, pages, 3)
	require.Len(t, pages[0], 2)
	require.Len(t, pages[1], 2)
	require.Len(t, pages[2], 1)
	require.Equal(t, []string{"scan 0 match * count 100", "scan 1 match * count 100"}, scanLines(f))
	for _, c := range f.snapshot() {
		require.Equal(t, ctx, c.ctx)
	}
}

// TestScanPagesEdges pins the edges of the shared cursor walk: an empty keyspace,
// an empty batch from build, a build error, an fn error and a SCAN error.
func TestScanPagesEdges(t *testing.T) {
	boom := errors.New("boom")
	build := func(_ context.Context, keys []string) (map[string]any, error) {
		out := map[string]any{}
		for _, k := range keys {
			out[k] = k
		}
		return out, nil
	}
	tests := []struct {
		name       string
		scan       [][]string
		fail       map[int]error
		build      func(context.Context, []string) (map[string]any, error)
		fn         func(map[string]any) error
		wantErr    error
		wantText   string
		wantBuilds int
		wantFns    int
	}{
		{"empty keyspace", nil, nil, build, func(map[string]any) error { return nil }, nil, "", 0, 0},
		{
			"empty batch skips fn",
			[][]string{{"a", "b"}},
			nil,
			func(context.Context, []string) (map[string]any, error) { return map[string]any{}, nil },
			func(map[string]any) error { return nil }, nil, "", 1, 0,
		},
		{
			"build error stops the walk",
			[][]string{{"a", "b", "c"}},
			nil,
			func(context.Context, []string) (map[string]any, error) { return nil, boom },
			func(map[string]any) error { return nil }, boom, "", 1, 0,
		},
		{
			"fn error stops the walk",
			[][]string{{"a", "b", "c"}},
			nil, build,
			func(map[string]any) error { return boom }, boom, "", 1, 1,
		},
		{
			"scan error skips the final flush",
			[][]string{{"a"}, {"b"}},
			map[int]error{1: boom},
			build,
			func(map[string]any) error { return nil }, boom, "redis scan", 0, 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRedis{scan: tt.scan, fail: tt.fail}
			store := newFakeStore(t, f, 2)
			builds, fns := 0, 0

			err := store.scanPages(context.Background(),
				func(ctx context.Context, keys []string) (map[string]any, error) {
					builds++
					return tt.build(ctx, keys)
				},
				func(batch map[string]any) error {
					fns++
					return tt.fn(batch)
				})

			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}
			if tt.wantText != "" {
				require.ErrorContains(t, err, tt.wantText)
			}
			require.Equal(t, tt.wantBuilds, builds)
			require.Equal(t, tt.wantFns, fns)
		})
	}
}

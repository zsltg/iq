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
	"github.com/zsltg/iq/internal/query"
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
			p := newPipeline(t)

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
	p := newPipeline(t)

	r, err := readerFor(context.Background(), p, typedKey{key: "k", typ: "TSDB-TYPE"}, numfmt.DecimalAuto)

	require.EqualError(t, err, `unsupported redis type "TSDB-TYPE" for key "k"`)
	require.Nil(t, r)
	require.Zero(t, p.Len())
}

// readPath is one of the two ways a Store reads a page of keys: Get, and the
// filtered read that ScanFiltered uses. Both share the pipelines and the error
// wraps, so the tests below run each case against both.
type readPath struct {
	name string
	read func(s *Store, ctx context.Context, keys []string) (map[string]any, error) //nolint:revive // ctx follows the receiver-like store here.
}

var readPaths = []readPath{
	{"Get", func(s *Store, ctx context.Context, keys []string) (map[string]any, error) {
		return s.Get(ctx, keys)
	}},
	{"getFiltered", func(s *Store, ctx context.Context, keys []string) (map[string]any, error) {
		return s.getFiltered(ctx, keys, rawpred.NewMatcher(nil))
	}},
}

// TestReadPathsReadTypesThenValuesInKeyOrder pins the two pipelines of a read:
// TYPE for each unique key in first-seen order, then each key's value command in
// the same order. Both carry the caller's context.
func TestReadPathsReadTypesThenValuesInKeyOrder(t *testing.T) {
	tests := []struct {
		path       readPath
		keys       map[string]fakeKey
		ask        []string
		want       map[string]any
		wantTypes  []string
		wantValues []string
	}{
		{
			readPaths[0],
			map[string]fakeKey{
				"b": {typ: "string", val: "vb"},
				"a": {typ: "hash", val: map[string]string{"f": "v"}},
			},
			[]string{"b", "a", "b", "gone"},
			map[string]any{"b": "vb", "a": map[string]any{"f": "v"}},
			[]string{"type b", "type a", "type gone"},
			[]string{"get b", "hgetall a"},
		},
		{
			readPaths[1],
			map[string]fakeKey{
				"s": {typ: "string", val: "v"},
				"j": {typ: "ReJSON-RL", val: `{"a":1}`},
			},
			[]string{"s", "j", "s"},
			map[string]any{"s": "v", "j": map[string]any{"a": 1}},
			[]string{"type s", "type j"},
			[]string{"get s", "JSON.GET j"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.path.name, func(t *testing.T) {
			ctx := markedContext()
			f := &fakeRedis{keys: tt.keys}
			store := newFakeStore(t, f, 10)

			got, err := tt.path.read(store, ctx, tt.ask)

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			calls := f.snapshot()
			require.Len(t, calls, 2)
			require.Equal(t, tt.wantTypes, calls[0].lines())
			require.Equal(t, tt.wantValues, calls[1].lines())
			for _, c := range calls {
				require.True(t, c.piped)
				require.Equal(t, ctx, c.ctx, "the caller's context reaches the pipeline")
			}
		})
	}
}

// TestReadPathsTreatAPerCommandNilAsAbsent pins that a value command that answers
// Nil (the key vanished after TYPE) drops that key and keeps the rest of the page.
func TestReadPathsTreatAPerCommandNilAsAbsent(t *testing.T) {
	tests := []struct {
		path     readPath
		goneType string
	}{
		{readPaths[0], "string"},
		{readPaths[1], "string"},
		{readPaths[1], "ReJSON-RL"},
	}
	for _, tt := range tests {
		t.Run(tt.path.name+"/"+tt.goneType, func(t *testing.T) {
			f := &fakeRedis{keys: map[string]fakeKey{
				"gone": {typ: tt.goneType, err: goredis.Nil},
				"here": {typ: "string", val: "v"},
			}}
			store := newFakeStore(t, f, 10)

			got, err := tt.path.read(store, context.Background(), []string{"gone", "here"})

			require.NoError(t, err)
			require.Equal(t, map[string]any{"here": "v"}, got)
		})
	}
}

// TestReadPathsWrapEachFailure pins the wraps of a read: a failed TYPE pipeline
// stops before the value pipeline, a failed value pipeline is wrapped, a reader
// error names its key, and an unsupported type stops before the value pipeline
// runs. A pipeline reports its first failed command, so the reader-error case
// puts a Nil key first and the failing reader second.
func TestReadPathsWrapEachFailure(t *testing.T) {
	boom := errors.New("boom")
	nilFirst := fakeKey{typ: "string", err: goredis.Nil}
	cases := []struct {
		name      string
		keys      map[string]fakeKey
		fail      map[int]error
		wantText  string
		wantIs    error
		wantCalls int
	}{
		{"type pipeline", map[string]fakeKey{"k": {typ: "string"}}, map[int]error{0: boom}, "redis type", boom, 1},
		{"value pipeline", map[string]fakeKey{"k": {typ: "string"}}, map[int]error{1: boom}, "redis read", boom, 2},
		{"reader error", map[string]fakeKey{"gone": nilFirst, "k": {typ: "hash", err: boom}}, nil, `read key "k"`, boom, 2},
		{"unsupported type", map[string]fakeKey{"k": {typ: "TSDB-TYPE"}}, nil, `unsupported redis type "TSDB-TYPE" for key "k"`, nil, 1},
	}
	for _, path := range readPaths {
		for _, tt := range cases {
			t.Run(path.name+"/"+tt.name, func(t *testing.T) {
				f := &fakeRedis{keys: tt.keys, fail: tt.fail}
				store := newFakeStore(t, f, 10)

				got, err := path.read(store, context.Background(), []string{"gone", "k"})

				require.ErrorContains(t, err, tt.wantText)
				if tt.wantIs != nil {
					require.ErrorIs(t, err, tt.wantIs)
				}
				require.Nil(t, got)
				require.Len(t, f.snapshot(), tt.wantCalls)
			})
		}
	}
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

// keyspaceWalk runs one of the two cursor walks, calling onPage for each page it
// delivers. Both walks share walkKeyPages, so the edge cases below run on both.
type keyspaceWalk struct {
	name string
	run  func(s *Store, ctx context.Context, onPage func() error) error //nolint:revive // ctx follows the store here.
}

var keyspaceWalks = []keyspaceWalk{
	{"scanPages", func(s *Store, ctx context.Context, onPage func() error) error {
		build := func(_ context.Context, keys []string) (map[string]any, error) {
			out := map[string]any{}
			for _, k := range keys {
				out[k] = k
			}
			return out, nil
		}
		return s.scanPages(ctx, build, func(map[string]any) error { return onPage() })
	}},
	{"TypedScan", func(s *Store, ctx context.Context, onPage func() error) error {
		return s.TypedScan(ctx, func([]query.Record) error { return onPage() })
	}},
}

// TestKeyspaceWalkStopsAtTheFirstError pins the edges that both cursor walks
// share: an empty keyspace calls nothing, an fn error ends the walk at once, and
// a SCAN error is wrapped and skips the final flush.
func TestKeyspaceWalkStopsAtTheFirstError(t *testing.T) {
	boom := errors.New("boom")
	keys := map[string]fakeKey{"a": {typ: "string", val: "v"}, "b": {typ: "string", val: "v"}, "c": {typ: "string", val: "v"}}
	cases := []struct {
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
		{"scan error", [][]string{{"a"}, {"b"}}, map[int]error{1: boom}, nil, "redis scan: ", boom, 0},
	}
	for _, w := range keyspaceWalks {
		for _, tt := range cases {
			t.Run(w.name+"/"+tt.name, func(t *testing.T) {
				f := &fakeRedis{keys: keys, scan: tt.scan, fail: tt.fail}
				store := newFakeStore(t, f, 2)
				pages := 0

				err := w.run(store, context.Background(), func() error {
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
}

// TestScanPagesBuildEdges pins what scanPages does with the result of build: an
// empty batch skips fn, and a build error ends the walk before fn runs.
func TestScanPagesBuildEdges(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name       string
		batch      map[string]any
		err        error
		wantErr    error
		wantBuilds int
	}{
		{"empty batch skips fn", map[string]any{}, nil, nil, 2},
		{"build error stops the walk", nil, boom, boom, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRedis{scan: [][]string{{"a", "b", "c"}}}
			store := newFakeStore(t, f, 2)
			builds, fns := 0, 0

			err := store.scanPages(context.Background(),
				func(context.Context, []string) (map[string]any, error) {
					builds++
					return tt.batch, tt.err
				},
				func(map[string]any) error {
					fns++
					return nil
				})

			require.ErrorIs(t, err, tt.wantErr)
			require.Equal(t, tt.wantBuilds, builds)
			require.Zero(t, fns)
		})
	}
}

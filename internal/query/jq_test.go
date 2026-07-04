package query_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// fakeKV is a query.KVStore double. It records the keys Get received and how it
// was scanned, and feeds ScanBatches from a fixed keyspace in pages of batchSize.
type fakeKV struct {
	values     map[string]any
	getErr     error
	scanErr    error
	scanKeys   []string // the keyspace ScanBatches walks, in this order
	batchSize  int      // page size; 0 means one page of everything
	gotGetKeys []string
	scanCalls  int // times ScanBatches was invoked
	batchCount int // pages fed to fn
}

func (f *fakeKV) Get(_ context.Context, keys []string) (map[string]any, error) {
	f.gotGetKeys = keys
	if f.getErr != nil {
		return nil, f.getErr
	}
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		out[k] = f.values[k]
	}
	return out, nil
}

func (f *fakeKV) ScanBatches(_ context.Context, fn func(map[string]any) error) error {
	f.scanCalls++
	if f.scanErr != nil {
		return f.scanErr
	}
	size := f.batchSize
	if size <= 0 {
		size = len(f.scanKeys)
	}
	for i := 0; i < len(f.scanKeys); i += size {
		end := min(i+size, len(f.scanKeys))
		batch := make(map[string]any, end-i)
		for _, k := range f.scanKeys[i:end] {
			batch[k] = f.values[k]
		}
		f.batchCount++
		if err := fn(batch); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeKV) Close() error { return nil }

// collect runs the engine and gathers every emitted value.
func collect(t *testing.T, store query.KVStore, src string, allowUnbounded bool) ([]any, error) {
	t.Helper()
	return collectOpts(t, store, src, query.RunOptions{Unbounded: allowUnbounded})
}

// collectOpts runs the engine with explicit options and gathers every value.
func collectOpts(t *testing.T, store query.KVStore, src string, opts query.RunOptions) ([]any, error) {
	t.Helper()
	var got []any
	err := query.NewJQEngine(store).Run(context.Background(), src, opts, func(v any) error {
		got = append(got, v)
		return nil
	})
	return got, err
}

// filterKV is a fakeKV that also implements FilteredScanner, recording the
// predicate it was asked to push.
type filterKV struct {
	fakeKV
	gotPred     predicate.Node
	filterCalls int
}

func (f *filterKV) ScanFiltered(_ context.Context, pred predicate.Node, fn func(map[string]any) error) error {
	f.filterCalls++
	f.gotPred = pred
	// Deliver the keyspace directly (a real store would pre-filter here); this
	// stays distinct from ScanBatches so a test can tell which path ran.
	batch := make(map[string]any, len(f.scanKeys))
	for _, k := range f.scanKeys {
		batch[k] = f.values[k]
	}
	return fn(batch)
}

func TestJQEngineFetchesReferencedKeys(t *testing.T) {
	store := &fakeKV{values: map[string]any{"book:1": map[string]any{"title": "Go"}}}

	got, err := collect(t, store, `.["book:1"].title`, false)

	require.NoError(t, err)
	require.Equal(t, []any{"Go"}, got)
	require.Equal(t, []string{"book:1"}, store.gotGetKeys)
	require.Zero(t, store.scanCalls, "a specific key must not trigger a scan")
}

func TestJQEngineStreamsInBatchesWithoutFlag(t *testing.T) {
	// .[] is streamable, so it runs page by page with no --unbounded, emitting
	// each value in scan order as its page arrives.
	store := &fakeKV{
		scanKeys:  []string{"b", "a"},
		values:    map[string]any{"a": 1, "b": 2},
		batchSize: 1,
	}

	got, err := collect(t, store, ".[]", false)

	require.NoError(t, err)
	require.Equal(t, 1, store.scanCalls)
	require.Equal(t, 2, store.batchCount, "one page per key")
	require.Equal(t, []any{2, 1}, got, "streamed in scan order, not key-sorted")
	require.Nil(t, store.gotGetKeys, "streaming does not go through Get")
}

func TestJQEngineRefusesHolisticScanWithoutFlag(t *testing.T) {
	store := &fakeKV{scanKeys: []string{"a"}, values: map[string]any{"a": 1}}

	_, err := collect(t, store, "keys", false)

	require.ErrorIs(t, err, query.ErrScanNotAllowed)
	require.Zero(t, store.scanCalls, "a refused scan must not touch the store")
}

func TestJQEngineMaterializesHolisticWithFlag(t *testing.T) {
	store := &fakeKV{
		scanKeys:  []string{"b", "a"},
		values:    map[string]any{"a": 1, "b": 2},
		batchSize: 1,
	}

	got, err := collect(t, store, "keys", true)

	require.NoError(t, err)
	require.Equal(t, 1, store.scanCalls)
	require.Equal(t, []any{[]any{"a", "b"}}, got, "keys over the merged keyspace, sorted")
}

func TestJQEngineStreamableMaterializesWithFlag(t *testing.T) {
	// With --unbounded, a streamable filter is materialized instead of batched,
	// so its output is key-sorted rather than in scan order.
	store := &fakeKV{
		scanKeys:  []string{"b", "a"},
		values:    map[string]any{"a": 1, "b": 2},
		batchSize: 1,
	}

	got, err := collect(t, store, ".[]", true)

	require.NoError(t, err)
	require.Equal(t, []any{1, 2}, got, "materialized run is key-sorted, not scan order")
}

func TestJQEngineCompilePushesPredicate(t *testing.T) {
	store := &filterKV{fakeKV: fakeKV{
		scanKeys: []string{"1", "2"},
		values: map[string]any{
			"1": map[string]any{"author": "K", "title": "A"},
			"2": map[string]any{"author": "K", "title": "B"},
		},
	}}

	got, err := collectOpts(t, store, `.[] | select(.author == "K") | .title`, query.RunOptions{Compile: true})

	require.NoError(t, err)
	require.Equal(t, 1, store.filterCalls, "the predicate was pushed to the store")
	require.Zero(t, store.scanCalls, "a pushed scan does not also do a full scan")
	require.Equal(t, predicate.Eq{Path: []string{"author"}, Value: "K"}, store.gotPred)
	require.Equal(t, []any{"A", "B"}, got)
}

func TestJQEngineCompileFallsBackWhenNotPushable(t *testing.T) {
	store := &filterKV{fakeKV: fakeKV{
		scanKeys: []string{"1"},
		values:   map[string]any{"1": map[string]any{"year": 2018}},
	}}

	// A negation does not compile, so the engine full-scans instead.
	got, err := collectOpts(t, store, ".[] | select(.year != 2015)", query.RunOptions{Compile: true})

	require.NoError(t, err)
	require.Zero(t, store.filterCalls, "an uncompilable predicate is not pushed")
	require.Equal(t, 1, store.scanCalls, "it falls back to a full scan")
	require.Equal(t, []any{map[string]any{"year": 2018}}, got)
}

func TestJQEngineCompileIgnoredWhenStoreCannotFilter(t *testing.T) {
	// A plain fakeKV is not a FilteredScanner, so --compile is a no-op.
	store := &fakeKV{
		scanKeys: []string{"1"},
		values:   map[string]any{"1": map[string]any{"author": "K"}},
	}

	got, err := collectOpts(t, store, `.[] | select(.author == "K")`, query.RunOptions{Compile: true})

	require.NoError(t, err)
	require.Equal(t, 1, store.scanCalls, "falls back to the plain scan path")
	require.Equal(t, []any{map[string]any{"author": "K"}}, got)
}

func TestJQEngineRejectsEmptyExpression(t *testing.T) {
	store := &fakeKV{}

	_, err := collect(t, store, "", true)

	require.ErrorIs(t, err, query.ErrEmptyExpression)
	require.Nil(t, store.gotGetKeys, "the store must not be touched for an empty expression")
}

func TestJQEnginePropagatesParseError(t *testing.T) {
	store := &fakeKV{}

	_, err := collect(t, store, ".[", false)

	require.Error(t, err)
	require.ErrorContains(t, err, "parse expression")
	require.Nil(t, store.gotGetKeys)
}

func TestJQEnginePropagatesStoreError(t *testing.T) {
	store := &fakeKV{getErr: errors.New("boom")}

	_, err := collect(t, store, ".a", false)

	require.Error(t, err)
	require.ErrorContains(t, err, "boom")
	require.ErrorContains(t, err, "fetch keys")
}

func TestJQEnginePropagatesScanError(t *testing.T) {
	store := &fakeKV{scanErr: errors.New("scan boom")}

	_, err := collect(t, store, ".[]", false)

	require.Error(t, err)
	require.ErrorContains(t, err, "scan boom")
	require.ErrorContains(t, err, "scan keys")
}

func TestJQEnginePropagatesRuntimeError(t *testing.T) {
	// .a is a string; arithmetic on a string is a jq runtime error.
	store := &fakeKV{values: map[string]any{"a": "not a number"}}

	_, err := collect(t, store, ".a + 1", false)

	require.Error(t, err)
	require.ErrorContains(t, err, "run expression")
}

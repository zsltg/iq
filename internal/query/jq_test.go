package query_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// fakeKV is a query.KVStore double. It records the keys Get received and whether
// ScanAll was called, and returns canned values.
type fakeKV struct {
	values     map[string]any
	getErr     error
	scanErr    error
	scanKeys   []string
	gotGetKeys []string
	scanCalls  int
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

func (f *fakeKV) ScanAll(_ context.Context) ([]string, error) {
	f.scanCalls++
	if f.scanErr != nil {
		return nil, f.scanErr
	}
	return f.scanKeys, nil
}

func (f *fakeKV) Close() error { return nil }

// collect runs the engine and gathers every emitted value.
func collect(t *testing.T, store query.KVStore, src string, allowScan bool) ([]any, error) {
	t.Helper()
	var got []any
	err := query.NewJQEngine(store).Run(context.Background(), src, allowScan, func(v any) error {
		got = append(got, v)
		return nil
	})
	return got, err
}

func TestJQEngineFetchesReferencedKeys(t *testing.T) {
	store := &fakeKV{values: map[string]any{"book:1": map[string]any{"title": "Go"}}}

	got, err := collect(t, store, `.["book:1"].title`, false)

	require.NoError(t, err)
	require.Equal(t, []any{"Go"}, got)
	require.Equal(t, []string{"book:1"}, store.gotGetKeys)
	require.Zero(t, store.scanCalls, "a specific key must not trigger a scan")
}

func TestJQEngineScansWhenAllowed(t *testing.T) {
	store := &fakeKV{
		scanKeys: []string{"a", "b"},
		values:   map[string]any{"a": "1", "b": "2"},
	}

	got, err := collect(t, store, ".", true)

	require.NoError(t, err)
	require.Equal(t, 1, store.scanCalls)
	require.Equal(t, []any{map[string]any{"a": "1", "b": "2"}}, got)
	require.ElementsMatch(t, []string{"a", "b"}, store.gotGetKeys)
}

func TestJQEngineRefusesScanWithoutPermission(t *testing.T) {
	store := &fakeKV{}

	_, err := collect(t, store, ".[]", false)

	require.ErrorIs(t, err, query.ErrScanNotAllowed)
	require.Nil(t, store.gotGetKeys, "a refused scan must not touch the store")
	require.Zero(t, store.scanCalls)
}

func TestJQEngineStreamsMultipleValues(t *testing.T) {
	store := &fakeKV{values: map[string]any{
		"a": "x",
		"b": "y",
	}}

	got, err := collect(t, store, ".a, .b", false)

	require.NoError(t, err)
	require.Equal(t, []any{"x", "y"}, got)
	require.Equal(t, []string{"a", "b"}, store.gotGetKeys)
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

	_, err := collect(t, store, ".", true)

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

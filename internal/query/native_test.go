package query_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// fakeNative is a NativeReader over fixed pages.
type fakeNative struct {
	pages [][]query.Record
	err   error
}

func (f fakeNative) NativeForm() query.Form { return query.FormDynamoJSON }

func (f fakeNative) NativeScan(_ context.Context, fn func([]query.Record) error) error {
	for _, p := range f.pages {
		if err := fn(p); err != nil {
			return err
		}
	}
	return f.err
}

func TestNativeStoreScansTheNativeValues(t *testing.T) {
	pages := [][]query.Record{
		{{Key: "a", Form: query.FormDynamoJSON, Value: map[string]any{"n": map[string]any{"N": "1.50"}}}},
		{{Key: "b", Form: query.FormDynamoJSON, Value: map[string]any{"n": map[string]any{"N": "2"}}}},
	}
	st := query.NewNativeStore(fakeNative{pages: pages}, nil)
	var out []any
	err := query.NewJQEngine(st).Run(context.Background(), ".[] | .n.N", query.RunOptions{Compile: true}, func(v any) error {
		out = append(out, v)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []any{"1.50", "2"}, out)
}

func TestNativeStoreHasNoPushdown(t *testing.T) {
	_, ok := query.NewNativeStore(fakeNative{}, nil).(query.FilteredScanner)
	require.False(t, ok)
}

func TestNativeStoreRefusesNamedKeys(t *testing.T) {
	st := query.NewNativeStore(fakeNative{}, nil)
	err := query.NewJQEngine(st).Run(context.Background(), `.["a"]`, query.RunOptions{}, func(any) error { return nil })
	require.ErrorIs(t, err, query.ErrNativeGet)
}

func TestNativeStoreStopsOnTheReaderError(t *testing.T) {
	boom := errors.New("boom")
	st := query.NewNativeStore(fakeNative{err: boom}, nil)
	err := st.ScanBatches(context.Background(), func(map[string]any) error { return nil })
	require.ErrorIs(t, err, boom)
}

func TestNativeStoreClose(t *testing.T) {
	closed := 0
	st := query.NewNativeStore(fakeNative{}, func() error { closed++; return nil })
	require.NoError(t, st.Close())
	require.Equal(t, 1, closed)
	require.NoError(t, query.NewNativeStore(fakeNative{}, nil).Close())
}

// ctxNative is a NativeReader that returns the error of its context.
type ctxNative struct{ fakeNative }

func (c ctxNative) NativeScan(ctx context.Context, _ func([]query.Record) error) error {
	return ctx.Err()
}

func TestNativeStorePassesTheContextToTheReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := query.NewNativeStore(ctxNative{}, nil)
	err := st.ScanBatches(ctx, func(map[string]any) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
}

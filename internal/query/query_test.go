package query_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// fakeStore is a query.Store double that records the args it received and
// returns canned values.
type fakeStore struct {
	result  any
	err     error
	gotArgs []string
	gotCtx  context.Context // the ctx Query was handed, so a dropped one is visible
	calls   int
}

func (f *fakeStore) Query(ctx context.Context, args []string) (any, error) {
	f.calls++
	f.gotArgs = args
	f.gotCtx = ctx
	return f.result, f.err
}

func (f *fakeStore) Close() error { return nil }

func TestRunnerForwardsArgsAndReturnsResult(t *testing.T) {
	store := &fakeStore{result: "hello"}
	ctx := context.WithValue(context.Background(), ctxMarker{}, "marker")

	got, err := query.NewRunner(store).Run(ctx, []string{"GET", "greeting"})

	require.NoError(t, err)
	require.Equal(t, "hello", got)
	require.Equal(t, []string{"GET", "greeting"}, store.gotArgs)
	// The store's call must stay bounded by the caller's context; a substituted
	// nil would silently unbound it.
	require.NotNil(t, store.gotCtx, "Query must receive a non-nil context")
	require.Equal(t, "marker", store.gotCtx.Value(ctxMarker{}), "the caller's context must reach Query")
}

func TestRunnerRejectsEmptyQuery(t *testing.T) {
	store := &fakeStore{}

	got, err := query.NewRunner(store).Run(context.Background(), nil)

	require.ErrorIs(t, err, query.ErrEmptyQuery)
	require.Nil(t, got)
	require.Zero(t, store.calls, "the store must not be touched for an empty query")
}

func TestRunnerPropagatesStoreError(t *testing.T) {
	store := &fakeStore{err: errors.New("boom")}

	got, err := query.NewRunner(store).Run(context.Background(), []string{"GET", "x"})

	require.Error(t, err)
	require.ErrorContains(t, err, "boom")
	require.ErrorContains(t, err, "GET", "the error should name the command for context")
	require.Nil(t, got)
	// Naming the command is done by wrapping, so the store's error has to stay
	// reachable underneath; formatting it in reads the same and severs errors.Is.
	require.ErrorIs(t, err, store.err, "the store error must stay unwrappable")
}

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
	// The store's call must stay bounded by the caller's context. A substituted
	// nil silently unbounds it.
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
	require.ErrorContains(t, err, "run query", "the error should say what failed")
	require.Nil(t, got)
	// The code names the command by wrapping, so the store's error has to stay
	// reachable underneath. Formatting it in reads the same and severs errors.Is.
	require.ErrorIs(t, err, store.err, "the store error must stay unwrappable")
}

func TestRunnerErrorDoesNotQuoteTheStatement(t *testing.T) {
	const secret = "s3cretToken"
	tests := []struct {
		name string
		args []string
	}{
		{name: "Mongo JSON command", args: []string{`{"insert":"u","documents":[{"pw":"` + secret + `"}]}`}},
		{name: "CQL statement", args: []string{"INSERT INTO u (pw) VALUES ('" + secret + "')"}},
		{name: "verb and operand", args: []string{"SET", "k", secret}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{err: errors.New("boom")}

			_, err := query.NewRunner(store).Run(context.Background(), tt.args)

			require.ErrorIs(t, err, store.err)
			require.NotContains(t, err.Error(), secret)
		})
	}
}

func TestRowLimit(t *testing.T) {
	tests := []struct {
		name   string
		n      int
		want   int
		wantOK bool
	}{
		{name: "a positive value is carried", n: 5, want: 5, wantOK: true},
		{name: "one is a limit", n: 1, want: 1, wantOK: true},
		{name: "zero sets no limit", n: 0},
		{name: "a negative value sets no limit", n: -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := query.RowLimit(query.WithRowLimit(context.Background(), tc.n))
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.want, got)
		})
	}
	t.Run("a bare context has no limit", func(t *testing.T) {
		_, ok := query.RowLimit(context.Background())
		require.False(t, ok)
	})
}

// TestUnderRowLimit pins when a driver may read one more row: always without a
// limit, and only below the limit with one.
func TestUnderRowLimit(t *testing.T) {
	limited := query.WithRowLimit(context.Background(), 2)
	tests := []struct {
		name string
		ctx  context.Context
		n    int
		want bool
	}{
		{"no limit, many rows", context.Background(), 1000, true},
		{"below the limit", limited, 1, true},
		{"at the limit", limited, 2, false},
		{"above the limit", limited, 3, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, query.UnderRowLimit(tc.ctx, tc.n))
		})
	}
}

// TestMaxValueElements pins the element limit that the Redis, HBase and RDB reads
// share.
func TestMaxValueElements(t *testing.T) {
	require.Equal(t, 1_000_000, query.MaxValueElements)
}

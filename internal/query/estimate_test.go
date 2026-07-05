package query_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// estimatorKV is a fakeKV that also implements Estimator, recording how often it
// was asked so a test can assert when the engine requests a total.
type estimatorKV struct {
	fakeKV
	estimate      int64
	estimateErr   error
	estimateCalls int
}

func (f *estimatorKV) EstimateCount(context.Context) (int64, error) {
	f.estimateCalls++
	if f.estimateErr != nil {
		return 0, f.estimateErr
	}
	return f.estimate, nil
}

// filterEstimatorKV pushes filters down and also estimates, so a test can assert
// a pushed-down scan skips the whole-keyspace estimate (it would overshoot).
type filterEstimatorKV struct {
	filterKV
	estimate      int64
	estimateCalls int
}

func (f *filterEstimatorKV) EstimateCount(context.Context) (int64, error) {
	f.estimateCalls++
	return f.estimate, nil
}

// TestJQEngineEstimateReportsTotal asserts OnEstimate fires once with the store's
// count before an unfiltered scan, and never for a bounded read or a pushed-down
// filtered scan.
func TestJQEngineEstimateReportsTotal(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		unbounded bool
		want      []int64
	}{
		{name: "streaming unfiltered reports estimate", src: ".[]", want: []int64{42}},
		{name: "materialized reports estimate", src: "keys", unbounded: true, want: []int64{42}},
		{name: "bounded never estimates", src: `.["a"]`, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &estimatorKV{
				fakeKV: fakeKV{
					scanKeys: []string{"a", "b"},
					values:   map[string]any{"a": 1, "b": 2},
				},
				estimate: 42,
			}
			var got []int64
			opts := query.RunOptions{
				Unbounded:  tc.unbounded,
				OnEstimate: func(n int64) { got = append(got, n) },
			}

			err := query.NewJQEngine(store).Run(context.Background(), tc.src, opts, func(any) error { return nil })

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			require.Len(t, got, store.estimateCalls, "OnEstimate fires exactly per EstimateCount call")
		})
	}
}

// TestJQEngineEstimateSkippedForPushedScan asserts a pushed-down filtered scan
// does not request the whole-keyspace estimate, which would overshoot the subset
// the store actually walks.
func TestJQEngineEstimateSkippedForPushedScan(t *testing.T) {
	store := &filterEstimatorKV{
		filterKV: filterKV{fakeKV: fakeKV{
			scanKeys: []string{"1"},
			values:   map[string]any{"1": map[string]any{"author": "K"}},
		}},
		estimate: 99,
	}
	var got []int64
	opts := query.RunOptions{Compile: true, OnEstimate: func(n int64) { got = append(got, n) }}

	err := query.NewJQEngine(store).Run(context.Background(), `.[] | select(.author == "K")`, opts, func(any) error { return nil })

	require.NoError(t, err)
	require.Equal(t, 1, store.filterCalls, "the scan was pushed down")
	require.Zero(t, store.estimateCalls, "a pushed-down scan does not ask for a whole-keyspace estimate")
	require.Nil(t, got, "no total is reported for a pushed-down scan")
}

// TestJQEngineEstimateBestEffort asserts the estimate never breaks the query: a
// non-estimator store, a nil callback, and a failing estimate all run to full
// results with no total, and a failing estimate is not requested twice.
func TestJQEngineEstimateBestEffort(t *testing.T) {
	t.Run("non-estimator store reports no total", func(t *testing.T) {
		store := &fakeKV{scanKeys: []string{"a"}, values: map[string]any{"a": 1}}
		var got []int64

		res, err := collectOpts(t, store, ".[]", query.RunOptions{OnEstimate: func(n int64) { got = append(got, n) }})

		require.NoError(t, err)
		require.Equal(t, []any{1}, res)
		require.Nil(t, got, "a store that is not an Estimator reports no total")
	})

	t.Run("nil callback never touches the store", func(t *testing.T) {
		store := &estimatorKV{
			fakeKV:   fakeKV{scanKeys: []string{"a"}, values: map[string]any{"a": 1}},
			estimate: 7,
		}

		err := query.NewJQEngine(store).Run(context.Background(), ".[]", query.RunOptions{}, func(any) error { return nil })

		require.NoError(t, err)
		require.Zero(t, store.estimateCalls, "a nil OnEstimate short-circuits before the store is asked")
	})

	t.Run("estimate error is swallowed and the scan still completes", func(t *testing.T) {
		store := &estimatorKV{
			fakeKV:      fakeKV{scanKeys: []string{"a", "b"}, values: map[string]any{"a": 1, "b": 2}},
			estimateErr: errors.New("count failed"),
		}
		var got []int64

		var res []any
		err := query.NewJQEngine(store).Run(context.Background(), ".[]", query.RunOptions{
			OnEstimate: func(n int64) { got = append(got, n) },
		}, func(v any) error { res = append(res, v); return nil })

		require.NoError(t, err, "a failed estimate must not fail the query")
		require.Len(t, res, 2, "the scan still streams every value")
		require.Nil(t, got, "a failed estimate reports no total")
		require.Equal(t, 1, store.estimateCalls, "the estimate is attempted once, not retried")
	})
}

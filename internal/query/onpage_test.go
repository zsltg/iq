package query_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// TestJQEngineOnPageTicksPerScannedPage asserts the progress hook fires once per
// scanned page with the page's item count on both scan paths, and never on a
// bounded read.
func TestJQEngineOnPageTicksPerScannedPage(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		unbounded bool
		batchSize int
		want      []int
	}{
		{name: "streaming ticks each page", src: ".[]", batchSize: 1, want: []int{1, 1, 1}},
		{name: "materialized ticks each page", src: "keys", unbounded: true, batchSize: 2, want: []int{2, 1}},
		{name: "bounded never ticks", src: `.["a"]`, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeKV{
				scanKeys:  []string{"a", "b", "c"},
				values:    map[string]any{"a": 1, "b": 2, "c": 3},
				batchSize: tc.batchSize,
			}
			var pages []int
			opts := query.RunOptions{
				Unbounded: tc.unbounded,
				OnPage:    func(n int) { pages = append(pages, n) },
			}
			_, err := collectOpts(t, store, tc.src, opts)
			require.NoError(t, err)
			require.Equal(t, tc.want, pages)
		})
	}
}

// TestJQEngineNilOnPageIsSafe asserts a scan runs normally when no progress hook
// is set (the default).
func TestJQEngineNilOnPageIsSafe(t *testing.T) {
	store := &fakeKV{
		scanKeys:  []string{"a", "b"},
		values:    map[string]any{"a": 1, "b": 2},
		batchSize: 1,
	}

	got, err := collectOpts(t, store, ".[]", query.RunOptions{})

	require.NoError(t, err)
	require.Len(t, got, 2)
}

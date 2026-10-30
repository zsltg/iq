package query_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestNoteNonFiniteWithoutSink(t *testing.T) {
	require.NotPanics(t, func() {
		query.NoteNonFinite(context.Background(), "k", "a", "b")
	})
}

func TestNoteNonFiniteReachesSink(t *testing.T) {
	type call struct{ key, field string }
	var got []call
	ctx := query.WithNonFiniteNotice(context.Background(), func(key, field string) {
		got = append(got, call{key, field})
	})

	query.NoteNonFinite(ctx, "k1", "price", "tags[2]")
	query.NoteNonFinite(ctx, "k2")

	require.Equal(t, []call{{"k1", "price"}, {"k1", "tags[2]"}}, got)
}

func TestNonFiniteSummaryText(t *testing.T) {
	tests := []struct {
		name  string
		calls [][2]string
		want  string
	}{
		{name: "no call gives no text"},
		{
			name:  "one call",
			calls: [][2]string{{"k1", "price"}},
			want:  `iq converted 1 value that JSON cannot hold (NaN or infinity) to strings; first: key "k1", field "price"`,
		},
		{
			name:  "the first call is named",
			calls: [][2]string{{"k1", "price"}, {"k2", "cost"}, {"k3", "x"}},
			want:  `iq converted 3 values that JSON cannot hold (NaN or infinity) to strings; first: key "k1", field "price"`,
		},
		{
			name:  "a key with a control character is quoted",
			calls: [][2]string{{"a\x1b[31mb", "f"}},
			want:  `iq converted 1 value that JSON cannot hold (NaN or infinity) to strings; first: key "a\x1b[31mb", field "f"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s query.NonFiniteSummary
			ctx := query.WithNonFiniteNotice(context.Background(), s.Sink())
			for _, c := range tt.calls {
				query.NoteNonFinite(ctx, c[0], c[1])
			}
			require.Equal(t, tt.want, s.Text())
		})
	}
}

func TestNonFiniteSummaryCountsConcurrentCalls(t *testing.T) {
	var s query.NonFiniteSummary
	sink := s.Sink()
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { sink("k", "f") })
	}
	wg.Wait()

	require.Contains(t, s.Text(), "iq converted 50 values")
}

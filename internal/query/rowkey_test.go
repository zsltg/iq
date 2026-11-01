package query_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestNoteRowKeyWithoutSink(t *testing.T) {
	require.NotPanics(t, func() {
		query.NoteRowKey(context.Background(), "/w==")
	})
}

func TestRowKeySummaryText(t *testing.T) {
	const fix = "; a Get, delete or copy with such a key reaches a different row. " +
		"Declare ?keytype=bytes on the source to address them exactly. First: "
	tests := []struct {
		name string
		keys []string
		want string
	}{
		{name: "no key gives no text"},
		{
			name: "one key",
			keys: []string{"/w=="},
			want: "iq showed 1 HBase row key that is not valid UTF-8 as base64" + fix + `"/w=="`,
		},
		{
			name: "the first key is named",
			keys: []string{"/w==", "AAE=", "gA=="},
			want: "iq showed 3 HBase row keys that are not valid UTF-8 as base64" + fix + `"/w=="`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s query.RowKeySummary
			ctx := query.WithRowKeyNotice(context.Background(), s.Sink())
			for _, k := range tt.keys {
				query.NoteRowKey(ctx, k)
			}
			require.Equal(t, tt.want, s.Text())
		})
	}
}

func TestRowKeySummaryCountsConcurrentCalls(t *testing.T) {
	var s query.RowKeySummary
	sink := s.Sink()
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { sink("/w==") })
	}
	wg.Wait()

	require.Contains(t, s.Text(), "iq showed 50 HBase row keys")
}

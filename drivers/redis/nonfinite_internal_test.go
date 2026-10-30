package redis

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

func TestZSetReaderReportsNonFiniteScores(t *testing.T) {
	type call struct{ key, field string }
	var got []call
	ctx := query.WithNonFiniteNotice(context.Background(), func(key, field string) {
		got = append(got, call{key, field})
	})
	pr := onePage(ctx)
	pr.key = "board"

	v, ok, err := zsetReader{pr, scanReply(ctx, "a", "1", "b", "inf", "c", "-inf")}.normalize()

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []any{
		map[string]any{"member": "c", "score": "-inf"},
		map[string]any{"member": "a", "score": 1.0},
		map[string]any{"member": "b", "score": "inf"},
	}, v)
	require.Equal(t, []call{{"board", "[0].score"}, {"board", "[2].score"}}, got)
}

func TestReaderForGivesZSetReaderItsKeyAndContext(t *testing.T) {
	ctx := markedContext()
	p := newPipeline(t)

	s := &Store{decimal: numfmt.DecimalAuto, limits: defaultLimits()}

	r, err := s.readerFor(ctx, p, typedKey{key: "board", typ: "zset"})

	require.NoError(t, err)
	z, ok := r.(zsetReader)
	require.True(t, ok)
	require.Equal(t, "board", z.key)
	require.Equal(t, ctx, z.ctx)
}

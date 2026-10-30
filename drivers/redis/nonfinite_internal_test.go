package redis

import (
	"context"
	"math"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestZSetReaderReportsNonFiniteScores(t *testing.T) {
	type call struct{ key, field string }
	var got []call
	ctx := query.WithNonFiniteNotice(context.Background(), func(key, field string) {
		got = append(got, call{key, field})
	})
	c := goredis.NewZSliceCmd(ctx)
	c.SetVal([]goredis.Z{
		{Member: "a", Score: 1},
		{Member: "b", Score: math.Inf(1)},
		{Member: "c", Score: math.NaN()},
	})

	v, ok, err := zsetReader{cmd: c, ctx: ctx, key: "board"}.normalize()

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []any{
		map[string]any{"member": "a", "score": 1.0},
		map[string]any{"member": "b", "score": "inf"},
		map[string]any{"member": "c", "score": "nan"},
	}, v)
	require.Equal(t, []call{{"board", "[1].score"}, {"board", "[2].score"}}, got)
}

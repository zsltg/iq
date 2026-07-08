package redis

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/selector"
)

func TestExplainPlan(t *testing.T) {
	t.Run("bounded keys pipeline TYPE then typed reads", func(t *testing.T) {
		got := ExplainPlan(selector.KeySet{Keys: []string{"a", "b", "c"}}, nil, false)
		require.Nil(t, got.Filter)
		require.Equal(t, []string{
			"pipeline TYPE for 3 requested key(s)",
			"then per key by type: GET / HGETALL / LRANGE 0 -1 / SMEMBERS / ZRANGE 0 -1 WITHSCORES / JSON.GET",
		}, got.Ops)
	})

	t.Run("scan uses SCAN MATCH star and never filters server-side", func(t *testing.T) {
		got := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, false)
		require.Nil(t, got.Filter)
		require.Contains(t, got.Ops[0], "SCAN 0 MATCH * COUNT 100")
		require.Contains(t, got.Ops, "no server-side filter — every key is read and filtered client-side")
	})
}

func TestExplainDelete(t *testing.T) {
	got, ok := ExplainDelete()
	require.True(t, ok)
	require.Len(t, got.Ops, 1)
	require.Contains(t, got.Ops[0], "DEL key...")
}

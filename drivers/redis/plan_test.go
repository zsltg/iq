package redis

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
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

	t.Run("scan reads every key client-side", func(t *testing.T) {
		got := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, false)
		require.Nil(t, got.Filter)
		require.Contains(t, got.Ops[0], "SCAN 0 MATCH * COUNT 100")
		require.Equal(t, query.StageNone, got.Server.State)
		require.Len(t, got.Ops, 3)
	})
}

func TestExplainPlanStages(t *testing.T) {
	server := query.StagePlan{State: query.StageNone, Note: "Redis filters nothing on the server"}
	tests := []struct {
		name string
		keys selector.KeySet
		pred predicate.Node
		want query.AccessPlan
	}{
		{
			name: "scan with a predicate prefilters",
			keys: selector.KeySet{Scan: true, Streamable: true},
			pred: predicate.Eq{Path: []string{"author", "name"}, Value: "Rob"},
			want: query.AccessPlan{
				Server:    server,
				Prefilter: query.StagePlan{State: query.StageOn, Scope: "rawpred on the raw bytes of RedisJSON values, before decode"},
			},
		},
		{
			name: "scan with a non-pushable predicate still prefilters",
			keys: selector.KeySet{Scan: true, Streamable: true},
			pred: predicate.Regex{Path: []string{"t"}, Pattern: "^x"},
			want: query.AccessPlan{
				Server:    server,
				Prefilter: query.StagePlan{State: query.StageOn, Scope: "rawpred on the raw bytes of RedisJSON values, before decode"},
			},
		},
		{
			name: "scan with a nil predicate",
			keys: selector.KeySet{Scan: true, Streamable: true},
			want: query.AccessPlan{
				Server:    server,
				Prefilter: query.StagePlan{State: query.StageOff, Note: "no compiled predicate"},
			},
		},
		{
			name: "bounded read has no stages",
			keys: selector.KeySet{Keys: []string{"a"}},
			pred: predicate.Eq{Path: []string{"a"}, Value: 1.0},
			want: query.AccessPlan{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExplainPlan(tt.keys, tt.pred, false)
			require.Nil(t, got.Filter)
			require.Equal(t, tt.want.Server, got.Server)
			require.Equal(t, tt.want.Prefilter, got.Prefilter)
		})
	}
}

func TestExplainDelete(t *testing.T) {
	got, ok := ExplainDelete()
	require.True(t, ok)
	require.Len(t, got.Ops, 1)
	require.Contains(t, got.Ops[0], "DEL key...")
}

package dynamodb

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

func TestExplainPlan(t *testing.T) {
	t.Run("bounded reads by key", func(t *testing.T) {
		p := ExplainPlan(selector.KeySet{Keys: []string{"1", "2"}}, nil, false)
		require.Equal(t, []string{"BatchGetItem: fetch 2 requested key(s)"}, p.Ops)
		require.Nil(t, p.Filter)
	})

	t.Run("streaming full scan", func(t *testing.T) {
		p := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, false)
		require.Contains(t, p.Ops[0], "full-table scan")
		require.Equal(t, "cursor streamed in pages of 100", p.Ops[1])
		require.Nil(t, p.Filter)
	})

	t.Run("scan with pushed filter", func(t *testing.T) {
		pred := predicate.Eq{Path: []string{"author"}, Value: "x"}
		p := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.Contains(t, p.Ops[0], "FilterExpression author = ?")
		require.Equal(t, map[string]any{"filter": "author = ?"}, p.Filter)
	})

	t.Run("unbounded materializes", func(t *testing.T) {
		p := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, true)
		require.Equal(t, "whole result materialized in memory", p.Ops[1])
	})

	t.Run("non-streamable materializes", func(t *testing.T) {
		p := ExplainPlan(selector.KeySet{Scan: true, Streamable: false}, nil, false)
		require.Equal(t, "whole result materialized in memory", p.Ops[1])
	})
}

func TestExplainWrite(t *testing.T) {
	require.Contains(t, ExplainWrite(query.Upsert).Ops[0], "upsert by primary key")
	require.Contains(t, ExplainWrite(query.InsertOnly).Ops[0], "skip existing")
}

func TestExplainClear(t *testing.T) {
	require.Contains(t, ExplainClear().Ops[0], "BatchWriteItem delete-all")
}

func TestExplainDrop(t *testing.T) {
	p, ok := ExplainDrop()
	require.True(t, ok)
	require.Contains(t, p.Ops[0], "DeleteTable")
}

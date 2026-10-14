package hbase

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

const planURI = "hbase://zk/?table=t"

func TestExplainPlan(t *testing.T) {
	t.Run("bounded get by key", func(t *testing.T) {
		plan, err := ExplainPlan(Source{URI: planURI}, selector.KeySet{Keys: []string{"1", "2"}}, nil, false)
		require.NoError(t, err)
		require.Contains(t, plan.Ops[0], "Get by row key")
		require.Contains(t, plan.Ops[0], "2 requested key(s)")
		require.Nil(t, plan.Filter)
	})

	t.Run("filtered scan lists pushed columns", func(t *testing.T) {
		pred := predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"}
		plan, err := ExplainPlan(Source{URI: planURI}, selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.NoError(t, err)
		require.Contains(t, plan.Ops[0], "SingleColumnValueFilter")
		require.Contains(t, plan.Ops[0], cellKey("cf", "author"))
		require.Equal(t, map[string]any{"columns": []string{cellKey("cf", "author")}}, plan.Filter)
	})
}

func TestExplainPlanAddress(t *testing.T) {
	scan := selector.KeySet{Scan: true, Streamable: true}

	t.Run("address replaces a bad saved table", func(t *testing.T) {
		plan, err := ExplainPlan(Source{URI: "hbase://zk/?table=bad/name", Address: "good"}, scan, nil, false)
		require.NoError(t, err)
		require.Contains(t, plan.Ops[0], "full-table scan")
	})

	t.Run("bad address with a valid saved table", func(t *testing.T) {
		_, err := ExplainPlan(Source{URI: planURI, Address: "bad/name"}, scan, nil, false)
		require.ErrorContains(t, err, "not a path")
	})
}

func TestExplainPlanStages(t *testing.T) {
	scan := selector.KeySet{Scan: true, Streamable: true}
	numeric := predicate.Eq{Path: []string{"cf", "author"}, Value: 1.0}
	serverOn := query.StagePlan{State: query.StageOn, Scope: "single column value filter"}
	serverOff := query.StagePlan{State: query.StageOff, Note: "no conjunct pushes"}
	tests := []struct {
		name       string
		uri        string
		keys       selector.KeySet
		pred       predicate.Node
		wantServer query.StagePlan
	}{
		{"number with no declared type", planURI, scan, numeric, serverOff},
		{"number with a declared int type", planURI + "&types=cf:author=int", scan, numeric, serverOn},
		{"string needs no declaration", planURI, scan, predicate.Eq{Path: []string{"cf", "author"}, Value: "x"}, serverOn},
		{"nil predicate", planURI, scan, nil, serverOff},
		{"bounded read", planURI, selector.KeySet{Keys: []string{"a"}}, numeric, query.StagePlan{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := ExplainPlan(Source{URI: tt.uri}, tt.keys, tt.pred, false)
			require.NoError(t, err)
			require.Equal(t, tt.wantServer, plan.Server)
			require.Equal(t, query.StagePlan{}, plan.Prefilter)
		})
	}

	t.Run("a malformed types parameter is an error", func(t *testing.T) {
		_, err := ExplainPlan(Source{URI: planURI + "&types=noequals"}, scan, numeric, false)
		require.ErrorContains(t, err, "explain hbase source")
		require.ErrorContains(t, err, `malformed ?types= entry "noequals"`)
	})

	t.Run("a bounded read still rejects a bad URI", func(t *testing.T) {
		_, err := ExplainPlan(Source{URI: "redis://x"}, selector.KeySet{Keys: []string{"a"}}, nil, false)
		require.Error(t, err)
	})
}

func TestExplainWrite(t *testing.T) {
	require.Contains(t, ExplainWrite(query.Upsert).Ops[0], "Put: upsert")
	require.Contains(t, ExplainWrite(query.InsertOnly).Ops[0], "CheckAndPut")
}

func TestExplainClearDrop(t *testing.T) {
	require.Contains(t, ExplainClear().Ops[0], "Delete per row")
	plan, ok := ExplainDrop()
	require.True(t, ok)
	require.Contains(t, plan.Ops[0], "DeleteTable")
}

func TestExplainDelete(t *testing.T) {
	plan, ok := ExplainDelete()
	require.True(t, ok)
	// The exists-only pre-read supplies the present-vs-absent split; the whole-row
	// Delete removes the named rows.
	require.Contains(t, plan.Ops[0], "exists-only Get")
	require.Contains(t, plan.Ops[1], "Delete (whole row)")
}

func TestExplainPlanStreamingMode(t *testing.T) {
	tests := []struct {
		name       string
		streamable bool
		unbounded  bool
		want       string
	}{
		{
			name:       "a streamable bounded query is paged",
			streamable: true,
			want:       "cursor streamed in pages of 100",
		},
		{
			name:       "an unbounded query materializes",
			streamable: true,
			unbounded:  true,
			want:       "whole result materialized in memory",
		},
		{
			name: "a non-streamable query materializes",
			want: "whole result materialized in memory",
		},
		{
			name:      "neither streamable nor bounded materializes",
			unbounded: true,
			want:      "whole result materialized in memory",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := ExplainPlan(Source{URI: planURI}, selector.KeySet{Scan: true, Streamable: tt.streamable}, nil, tt.unbounded)
			require.NoError(t, err)
			require.Equal(t, []string{
				"Scan: full-table scan (every row read, filtered client-side)",
				tt.want,
			}, plan.Ops)
		})
	}
}

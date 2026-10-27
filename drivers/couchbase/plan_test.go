package couchbase

import (
	"errors"
	"strings"
	"testing"

	"github.com/couchbase/gocb/v2"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

func TestExplainPlan(t *testing.T) {
	t.Run("bounded fetches by id", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: false, Keys: []string{"1", "2"}}, nil, false, numfmt.DecimalAuto)
		require.Len(t, plan.Ops, 1)
		require.Contains(t, plan.Ops[0], "KV bulk get")
		require.Equal(t, []string{"1", "2"}, plan.Filter["keys"])
	})

	t.Run("scan page size is 100", func(t *testing.T) {
		// The plan prints the page size the scan uses. A literal pins the number.
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, false, numfmt.DecimalAuto)
		require.Contains(t, plan.Ops[0], "pages of 100 ")
		require.Contains(t, plan.Ops[1], "batches of 100")
	})

	t.Run("unfiltered scan streams in pages", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, false, numfmt.DecimalAuto)
		require.Contains(t, plan.Ops[0], "keyset scan")
		require.Nil(t, plan.Filter)
		require.Contains(t, plan.Ops[1], "streamed in batches")
	})

	t.Run("narrowing predicate shows the pushed where", func(t *testing.T) {
		pred := predicate.Eq{Path: []string{"year"}, Value: 2017}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false, numfmt.DecimalAuto)
		require.Contains(t, plan.Ops[0], "pre-filter")
		require.Equal(t, "`t`.`year` = $p0", plan.Filter["where"])
		require.Contains(t, plan.Filter["params"], "p0")
	})

	t.Run("a narrowing push never names the prefilter in the ops", func(t *testing.T) {
		pred := predicate.And{
			predicate.Eq{Path: []string{"year"}, Value: 2017},
			predicate.Ne{Path: []string{"lang"}, Value: "go"},
		}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false, numfmt.DecimalAuto)
		require.NotContains(t, plan.Ops[0], "rawpred")
		require.Contains(t, plan.Ops[0], "WHERE <pushed>")
		require.Equal(t, "`t`.`year` = $p0", plan.Filter["where"])
	})

	t.Run("non-narrowing predicate is a full scan", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, predicate.Ne{Path: []string{"year"}, Value: 2017}, false, numfmt.DecimalAuto)
		require.NotContains(t, plan.Ops[0], "rawpred")
		require.NotContains(t, plan.Ops[0], "WHERE <pushed>")
		require.Nil(t, plan.Filter)
	})

	t.Run("narrowing predicate with no params omits the params key", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, predicate.Exists{Path: []string{"tags"}}, false, numfmt.DecimalAuto)
		require.Equal(t, "`t`.`tags` IS NOT MISSING", plan.Filter["where"])
		require.NotContains(t, plan.Filter, "params")
	})

	t.Run("unbounded materializes", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, true, numfmt.DecimalAuto)
		require.Contains(t, plan.Ops[1], "materialized")
	})

	t.Run("a non-streamable query materializes", func(t *testing.T) {
		// Either condition materializes on its own: a bounded query whose shape the
		// engine cannot stream holds the whole result in memory as well.
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: false}, nil, false, numfmt.DecimalAuto)
		require.Contains(t, plan.Ops[1], "materialized")
	})
}

func TestExplainWrite(t *testing.T) {
	upsert := ExplainWrite(query.Upsert)
	require.Contains(t, strings.Join(upsert.Ops, " "), "upsert")
	require.Contains(t, strings.Join(upsert.Ops, " "), "overwrite accounting")

	insert := ExplainWrite(query.InsertOnly)
	require.Contains(t, insert.Ops[0], "skip existing")
}

func TestExplainClearDrop(t *testing.T) {
	require.Contains(t, ExplainClear().Ops[0], "DELETE FROM")
	plan, ok := ExplainDrop()
	require.True(t, ok)
	require.Contains(t, plan.Ops[0], "DropCollection")
}

func TestFormatRaw(t *testing.T) {
	s := &Store{}
	out := s.FormatRaw(map[string]any{"n": 1}, false)
	require.JSONEq(t, `{"n":1}`, out)
}

func TestQueryErrorNoIndexHint(t *testing.T) {
	s := &Store{bucket: "b", scope: "_default", coll: "orders"}

	t.Run("code 4000 gets the create-index hint", func(t *testing.T) {
		qerr := &gocb.QueryError{Errors: []gocb.QueryErrorDesc{{Code: queryNoIndex, Message: "No index available"}}}
		err := s.queryError("couchbase scan", qerr)
		require.Contains(t, err.Error(), "CREATE PRIMARY INDEX ON `b`.`_default`.`orders`")
		require.ErrorIs(t, err, qerr, "the hint adds context and keeps the cause reachable")
	})

	// The first row above builds its code from the constant, so these rows pin the number.
	for _, tt := range []struct {
		name     string
		code     uint32
		wantHint bool
	}{
		{name: "code 4000 is the no-index code", code: 4000, wantHint: true},
		{name: "code 3999 is another error", code: 3999},
		{name: "code 4001 is another error", code: 4001},
	} {
		t.Run(tt.name, func(t *testing.T) {
			qerr := &gocb.QueryError{Errors: []gocb.QueryErrorDesc{{Code: tt.code, Message: "boom"}}}
			err := s.queryError("couchbase scan", qerr)
			require.Equal(t, tt.wantHint, strings.Contains(err.Error(), "CREATE PRIMARY INDEX"))
		})
	}

	t.Run("other errors are wrapped without the hint", func(t *testing.T) {
		err := s.queryError("couchbase scan", errors.New("boom"))
		require.Contains(t, err.Error(), "couchbase scan: boom")
		require.NotContains(t, err.Error(), "CREATE PRIMARY INDEX")
	})
}

func TestExplainPlanStages(t *testing.T) {
	serverOn := query.StagePlan{State: query.StageOn, Scope: "sql++ where"}
	serverOff := query.StagePlan{State: query.StageOff, Note: "no conjunct pushes"}
	prefilterOn := query.StagePlan{State: query.StageOn, Scope: "rawpred on the raw value of each row, before decode"}
	scan := selector.KeySet{Scan: true, Streamable: true}
	tests := []struct {
		name          string
		keys          selector.KeySet
		pred          predicate.Node
		wantServer    query.StagePlan
		wantPrefilter query.StagePlan
	}{
		{
			name:          "exact where turns the prefilter off",
			keys:          scan,
			pred:          predicate.Eq{Path: []string{"year"}, Value: 2017},
			wantServer:    serverOn,
			wantPrefilter: query.StagePlan{State: query.StageOff, Note: "the WHERE captures the predicate exactly"},
		},
		{
			name: "equality with a declined regex keeps the prefilter on",
			keys: scan,
			pred: predicate.And{
				predicate.Eq{Path: []string{"year"}, Value: 2017},
				predicate.Regex{Path: []string{"t"}, Pattern: "^x"},
			},
			wantServer:    serverOn,
			wantPrefilter: prefilterOn,
		},
		{
			name:          "nothing pushable",
			keys:          scan,
			pred:          predicate.Ne{Path: []string{"year"}, Value: 2017},
			wantServer:    serverOff,
			wantPrefilter: prefilterOn,
		},
		{
			name:          "nil predicate",
			keys:          scan,
			wantServer:    serverOff,
			wantPrefilter: query.StagePlan{State: query.StageOff, Note: "no compiled predicate"},
		},
		{
			name: "bounded read",
			keys: selector.KeySet{Keys: []string{"1"}},
			pred: predicate.Eq{Path: []string{"year"}, Value: 2017},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := ExplainPlan(tt.keys, tt.pred, false, numfmt.DecimalAuto)
			require.Equal(t, tt.wantServer, plan.Server)
			require.Equal(t, tt.wantPrefilter, plan.Prefilter)
		})
	}
}

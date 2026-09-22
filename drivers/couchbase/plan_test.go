package couchbase

import (
	"errors"
	"strings"
	"testing"

	"github.com/couchbase/gocb/v2"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

func TestExplainPlan(t *testing.T) {
	t.Run("bounded fetches by id", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: false, Keys: []string{"1", "2"}}, nil, false)
		require.Len(t, plan.Ops, 1)
		require.Contains(t, plan.Ops[0], "KV bulk get")
		require.Equal(t, []string{"1", "2"}, plan.Filter["keys"])
	})

	t.Run("unfiltered scan streams in pages", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, false)
		require.Contains(t, plan.Ops[0], "keyset scan")
		require.Nil(t, plan.Filter)
		require.Contains(t, plan.Ops[1], "streamed in batches")
	})

	t.Run("narrowing predicate shows the pushed where", func(t *testing.T) {
		pred := predicate.Eq{Path: []string{"year"}, Value: 2017}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.Contains(t, plan.Ops[0], "pre-filter")
		require.Equal(t, "`t`.`year` = $p0", plan.Filter["where"])
		require.Contains(t, plan.Filter["params"], "p0")
	})

	t.Run("exact push describes no client-side prefilter", func(t *testing.T) {
		// An exact push (equality) returns precisely jq's matching set, so ExplainPlan must
		// not claim a client-side prefilter runs — the phrase distinguishes this branch from
		// both the partial and the non-narrowing wordings, which do mention it.
		pred := predicate.Eq{Path: []string{"year"}, Value: 2017}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.NotContains(t, plan.Ops[0], "raw-byte prefilter")
		require.Equal(t, "`t`.`year` = $p0", plan.Filter["where"])
	})

	t.Run("partial push describes the where and the residual prefilter", func(t *testing.T) {
		// An And whose != conjunct the server drops: it narrows on the equality, and the
		// residual is trimmed by the client-side raw-byte prefilter. "residual" is the phrase
		// unique to this branch (the non-narrowing wording says "provable non-matches"), so it
		// pins the exact-vs-partial split, not a shared prefix.
		pred := predicate.And{
			predicate.Eq{Path: []string{"year"}, Value: 2017},
			predicate.Ne{Path: []string{"lang"}, Value: "go"},
		}
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, pred, false)
		require.Contains(t, plan.Ops[0], "raw-byte prefilter")
		require.Contains(t, plan.Ops[0], "residual")
		require.Equal(t, "`t`.`year` = $p0", plan.Filter["where"])
	})

	t.Run("non-narrowing predicate is a full scan with the prefilter", func(t *testing.T) {
		// Nothing narrows, so the plan is a full keyset scan trimmed by the prefilter.
		// "provable non-matches" is the phrase unique to this branch, distinguishing it from
		// the partial wording's "residual" and pinning the default (non-narrowing) arm.
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, predicate.Ne{Path: []string{"year"}, Value: 2017}, false)
		require.Contains(t, plan.Ops[0], "raw-byte prefilter")
		require.Contains(t, plan.Ops[0], "provable non-matches")
		require.NotContains(t, plan.Ops[0], "WHERE <pushed>")
		require.Nil(t, plan.Filter)
	})

	t.Run("narrowing predicate with no params omits the params key", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, predicate.Exists{Path: []string{"tags"}}, false)
		require.Equal(t, "`t`.`tags` IS NOT MISSING", plan.Filter["where"])
		require.NotContains(t, plan.Filter, "params")
	})

	t.Run("unbounded materializes", func(t *testing.T) {
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, true)
		require.Contains(t, plan.Ops[1], "materialized")
	})

	t.Run("a non-streamable query materializes", func(t *testing.T) {
		// Either condition materializes on its own: a bounded query whose shape the
		// engine cannot stream holds the whole result in memory as well.
		plan := ExplainPlan(selector.KeySet{Scan: true, Streamable: false}, nil, false)
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

	t.Run("other errors are wrapped without the hint", func(t *testing.T) {
		err := s.queryError("couchbase scan", errors.New("boom"))
		require.Contains(t, err.Error(), "couchbase scan: boom")
		require.NotContains(t, err.Error(), "CREATE PRIMARY INDEX")
	})
}

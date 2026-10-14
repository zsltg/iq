package elasticsearch

import (
	"fmt"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// ExplainPlan describes the Elasticsearch calls this driver would make for a
// classified query, without connecting. A bounded query fetches the named documents
// by _id with _mget. A scan pages a point-in-time with search_after, with a bool
// query when the pushed predicate narrows. It does not connect, so it does not
// resolve the equality gate (a term is pushed only on an exactly matchable field).
// The shown filter is the query that the driver attempts, and the description says
// so.
func ExplainPlan(keys selector.KeySet, pred predicate.Node, unbounded bool) query.AccessPlan {
	if !keys.Scan {
		return query.AccessPlan{
			Ops:    []string{fmt.Sprintf("_mget: fetch %d requested id(s)", len(keys.Keys))},
			Filter: map[string]any{"ids": keys.Keys},
		}
	}

	var filter map[string]any
	desc := "_search + search_after (point-in-time keyset): full-index scan (every document read, filtered client-side)"
	if pred != nil {
		if q, narrowing := explainQuery(pred); narrowing {
			filter = q
			desc = "_search(<bool>): server-side pre-filter (equality on an exactly-mapped field and existence), then the full jq re-runs client-side"
		}
	}
	mode := fmt.Sprintf("pages streamed in batches of %d", scanBatch)
	if unbounded || !keys.Streamable {
		mode = "whole result materialized in memory"
	}
	server := query.StagePlan{State: query.StageOff, Note: query.NoPushNote}
	if filter != nil {
		server = query.StagePlan{
			State: query.StageConditional,
			Scope: "elasticsearch bool query",
			Note:  "each equality field is exactly mapped and its literal type matches",
		}
	}
	return query.AccessPlan{Ops: []string{desc, mode}, Filter: filter, Server: server, Prefilter: prefilterStage(pred)}
}

// prefilterStage decides the plan's prefilter state from the whole predicate, with no
// connection. It agrees with prefilterMatcher wherever the answer does not need the
// live mapping. The prefilter is off for a nil predicate and for a predicate that
// reads _id. It is on if a leaf is never exact. Else the mapping decides, so the
// state is conditional.
func prefilterStage(pred predicate.Node) query.StagePlan {
	const scope = "rawpred on the raw _source of each hit, before decode"
	switch {
	case pred == nil:
		return query.StagePlan{State: query.StageOff, Note: "no compiled predicate"}
	case referencesInjectedID(pred):
		return query.StagePlan{State: query.StageOff, Note: "the predicate reads _id, which the raw _source does not carry"}
	case neverExact(pred):
		return query.StagePlan{State: query.StageOn, Scope: scope}
	default:
		return query.StagePlan{
			State: query.StageConditional,
			Scope: scope,
			Note:  "unless every compiled equality translates to an exact term under the live mapping",
		}
	}
}

// explainQuery is the connection-free mirror of Store.toQuery. It pushes the same
// node kinds (equality, existence, and their and/or combinations). It assumes that an
// equality's field is exactly matchable, because only the live mapping decides. This
// is the optimistic plan that the --explain caveat describes.
func explainQuery(n predicate.Node) (map[string]any, bool) {
	switch t := n.(type) {
	case predicate.Eq:
		if t.Value == nil || len(t.Path) != 1 {
			return nil, false
		}
		return map[string]any{"term": map[string]any{field(t.Path): t.Value}}, true
	case predicate.Exists:
		return existsQuery(field(t.Path)), true
	case predicate.NotExists:
		return map[string]any{"bool": map[string]any{"must_not": existsQuery(field(t.Path))}}, true
	case predicate.And:
		var parts []any
		for _, child := range t {
			if q, ok := explainQuery(child); ok {
				parts = append(parts, q)
			}
		}
		switch len(parts) {
		case 0:
			return nil, false
		case 1:
			return parts[0].(map[string]any), true
		default:
			return map[string]any{"bool": map[string]any{"must": parts}}, true
		}
	case predicate.Or:
		parts := make([]any, 0, len(t))
		for _, child := range t {
			q, ok := explainQuery(child)
			if !ok {
				return nil, false
			}
			parts = append(parts, q)
		}
		if len(parts) == 0 {
			return nil, false
		}
		return map[string]any{"bool": map[string]any{"should": parts, "minimum_should_match": 1}}, true
	default:
		return nil, false
	}
}

// ExplainWrite describes, without connecting, the write a copy into this index would
// make: a refreshing _bulk that indexes each document by _id (replace-upsert), or one
// that creates new _ids and skips existing ones.
func ExplainWrite(mode query.WriteMode) query.AccessPlan {
	op := "_bulk (refresh): index by _id (replace-upsert)"
	if mode == query.InsertOnly {
		op = "_bulk (refresh): create new _ids, skip existing (conflict)"
	}
	return query.AccessPlan{Ops: []string{op}}
}

// ExplainClear describes the `iq data clear` op for an index: delete every document
// with a match-all _delete_by_query, keeping the index and its mapping.
func ExplainClear() query.AccessPlan {
	return query.AccessPlan{Ops: []string{"_delete_by_query {match_all} (refresh): empty the index, keep its mapping"}}
}

// ExplainDrop describes the `iq data drop` op for an index and reports it supported —
// an Elasticsearch index is a removable container.
func ExplainDrop() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{"DELETE /{index}: remove the index and everything in it"}}, true
}

// ExplainDelete describes the `iq data delete` op for an index: a _bulk of delete
// actions by _id, whose per-item result ("deleted"/"not_found") is exact.
func ExplainDelete() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{"_bulk {delete:{_id}} (refresh): remove the named documents (per-item deleted vs not_found is exact)"}}, true
}

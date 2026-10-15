package mongo

import (
	"fmt"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// ExplainPlan describes the MongoDB calls that this driver makes if it runs a
// classified query, without connecting. A bounded query fetches the named documents
// by _id. A scan runs a find. The find is pre-filtered by the pushed predicate when
// one compiles (else a full-collection scan). It reuses toFilter, so the shown
// filter is exactly what the server receives if the query runs.
func ExplainPlan(keys selector.KeySet, pred predicate.Node, unbounded bool) query.AccessPlan {
	if !keys.Scan {
		return query.AccessPlan{
			Ops:    []string{fmt.Sprintf("find by _id: fetch %d requested key(s)", len(keys.Keys))},
			Filter: map[string]any{"_id": map[string]any{"$in": keys.Keys}},
		}
	}

	var filter map[string]any
	desc := "find({}): full-collection scan (every document read, filtered client-side)"
	if pred != nil {
		filter = toFilter(pred)
		desc = "find(<filter>): server-side pre-filter, then the full jq re-runs client-side"
	}
	mode := fmt.Sprintf("cursor streamed in batches of %d", scanBatch)
	if unbounded || !keys.Streamable {
		mode = "whole result materialized in memory"
	}
	return query.AccessPlan{
		Ops:    []string{desc, mode},
		Filter: filter,
		Server: query.ServerStage("mongo find", filter != nil),
	}
}

// ExplainWrite describes, without connecting, the write that a copy into this
// collection makes if it runs. The write is an unordered bulk replace-upsert by
// _id, or an unordered insert that skips existing _ids.
func ExplainWrite(mode query.WriteMode) query.AccessPlan {
	op := fmt.Sprintf("bulkWrite: replaceOne upsert by _id, batches of %d", scanBatch)
	if mode == query.InsertOnly {
		op = fmt.Sprintf("insertMany (unordered): insert new _ids, skip existing, batches of %d", scanBatch)
	}
	return query.AccessPlan{Ops: []string{op}}
}

// ExplainClear describes the `iq data clear` op for a collection.
func ExplainClear() query.AccessPlan {
	return query.AccessPlan{Ops: []string{"deleteMany({}): empty the collection, keep its indexes"}}
}

// ExplainDrop describes the `iq data drop` op for a collection and reports it
// supported. A MongoDB collection is a removable container.
func ExplainDrop() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{"drop(): remove the collection and its indexes"}}, true
}

// ExplainDelete describes the `iq data delete` op for a collection: a chunked
// deleteMany over the named _ids, whose DeletedCount is exact.
func ExplainDelete() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{fmt.Sprintf("deleteMany({_id: {$in: [...]}}), batches of %d: remove the named keys (deletedCount is exact)", scanBatch)}}, true
}

package couchbase

import (
	"fmt"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// ExplainPlan describes the Couchbase calls this driver would make for a classified
// query, without connecting. A bounded query fetches the named documents by ID via a
// KV bulk get; a scan walks a SQL++ keyset scan ordered by document ID, pre-filtered by
// the pushed predicate when one narrows, else unfiltered. It reuses toWhere so the shown
// WHERE is exactly what the query service would receive, and mirrors ScanFiltered's own
// decision: unless the WHERE captures the predicate exactly, a client-side raw-byte
// prefilter trims whatever the server could not exclude before decode.
func ExplainPlan(keys selector.KeySet, pred predicate.Node, unbounded bool) query.AccessPlan {
	if !keys.Scan {
		return query.AccessPlan{
			Ops:    []string{fmt.Sprintf("KV bulk get: fetch %d requested key(s) by document ID", len(keys.Keys))},
			Filter: map[string]any{"keys": keys.Keys},
		}
	}

	var filter map[string]any
	desc := fmt.Sprintf("SELECT META(t).id, t FROM <keyspace> t ORDER BY META(t).id: keyset scan in pages of %d (every document read, filtered client-side)", scanBatch)
	if pred != nil {
		where, params, narrowing := toWhere(pred)
		if narrowing {
			filter = map[string]any{"where": where}
			if len(params) > 0 {
				filter["params"] = params
			}
		}
		// exactWhere(pred) implies narrowing (every exactly-pushed node narrows), so the
		// arms are ordered to keep each condition load-bearing on its own: !narrowing first
		// selects the non-narrowing full scan, then exactWhere splits the narrowing case into
		// the exact push and the partial (dropped-conjunct) push. A flat
		// `narrowing && exactWhere` first arm would make the narrowing operand redundant (an
		// unkillable, equivalent mutation), so it is deliberately not written that way.
		switch {
		case !narrowing:
			// Nothing narrows (!=, regex, …): a full keyset scan whose rows the
			// client-side raw-byte prefilter trims before decode.
			desc = fmt.Sprintf("SELECT META(t).id, t FROM <keyspace> t ORDER BY META(t).id: keyset scan in pages of %d, a client-side raw-byte prefilter (rawpred) drops provable non-matches before decode, then the full jq re-runs client-side", scanBatch)
		case exactWhere(pred):
			// The WHERE captures the predicate exactly, so no client-side prefilter runs.
			desc = fmt.Sprintf("SELECT META(t).id, t FROM <keyspace> t WHERE <pushed> ORDER BY META(t).id: server-side SQL++ pre-filter in pages of %d, then the full jq re-runs client-side", scanBatch)
		default:
			// A conjunct was dropped: the server narrows on the pushable part and a
			// client-side raw-byte prefilter trims the residual before decode.
			desc = fmt.Sprintf("SELECT META(t).id, t FROM <keyspace> t WHERE <pushed> ORDER BY META(t).id: server-side SQL++ pre-filter in pages of %d, then a client-side raw-byte prefilter (rawpred) drops the residual before decode and the full jq re-runs client-side", scanBatch)
		}
	}
	mode := fmt.Sprintf("pages streamed in batches of %d", scanBatch)
	if unbounded || !keys.Streamable {
		mode = "whole result materialized in memory"
	}
	return query.AccessPlan{Ops: []string{desc, mode}, Filter: filter}
}

// ExplainWrite describes, without connecting, the write a copy into this collection
// would make: a KV bulk upsert by document ID (reading which keys pre-exist first, to
// count overwrites), or a KV bulk insert that skips existing IDs.
func ExplainWrite(mode query.WriteMode) query.AccessPlan {
	if mode == query.InsertOnly {
		return query.AccessPlan{Ops: []string{"KV bulk insert: insert new document IDs, skip existing (ErrDocumentExists)"}}
	}
	return query.AccessPlan{Ops: []string{
		"KV bulk get: read which keys already exist (overwrite accounting)",
		"KV bulk upsert: write every document by ID",
	}}
}

// ExplainClear describes the `iq data clear` op for a collection: a parameterless SQL++
// DELETE that empties the keyspace but keeps it.
func ExplainClear() query.AccessPlan {
	return query.AccessPlan{Ops: []string{"DELETE FROM <keyspace>: empty the collection, keep it"}}
}

// ExplainDrop describes the `iq data drop` op for a collection and reports it supported
// — a Couchbase collection is a removable container (the default collection excepted at
// run time).
func ExplainDrop() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{"DropCollection: remove the collection and everything in it"}}, true
}

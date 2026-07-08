package couchdb

import (
	"fmt"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// ExplainPlan describes the CouchDB calls this driver would make for a classified
// query, without connecting. A bounded query fetches the named documents by _id via
// _all_docs; a scan runs a Mango _find pre-filtered by the pushed predicate when one
// narrows, else a full _all_docs walk. It reuses toSelector so the shown filter is
// exactly what the server would receive.
func ExplainPlan(keys selector.KeySet, pred predicate.Node, unbounded bool) query.AccessPlan {
	if !keys.Scan {
		return query.AccessPlan{
			Ops:    []string{fmt.Sprintf("_all_docs?keys: fetch %d requested key(s)", len(keys.Keys))},
			Filter: map[string]any{"keys": keys.Keys},
		}
	}

	var filter map[string]any
	desc := "_all_docs?include_docs=true: full-database scan (every document read, filtered client-side)"
	if pred != nil {
		if sel, narrowing := toSelector(pred); narrowing {
			filter = sel
			desc = "_find(<selector>): server-side Mango pre-filter, then the full jq re-runs client-side"
		}
	}
	mode := fmt.Sprintf("pages streamed in batches of %d", scanBatch)
	if unbounded || !keys.Streamable {
		mode = "whole result materialized in memory"
	}
	return query.AccessPlan{Ops: []string{desc, mode}, Filter: filter}
}

// ExplainWrite describes, without connecting, the write a copy into this database
// would make: an unordered _bulk_docs replace-upsert by _id (fetching current revs
// first), or an unordered _bulk_docs insert that skips existing _ids.
func ExplainWrite(mode query.WriteMode) query.AccessPlan {
	op := fmt.Sprintf("_bulk_docs: replace-upsert by _id (read current _rev first), batches of %d", scanBatch)
	if mode == query.InsertOnly {
		op = fmt.Sprintf("_bulk_docs: insert new _ids, skip existing (conflict), batches of %d", scanBatch)
	}
	return query.AccessPlan{Ops: []string{op}}
}

// ExplainClear describes the `iq data clear` op for a database: bulk-delete every
// non-design document by _rev, keeping the database and its design documents.
func ExplainClear() query.AccessPlan {
	return query.AccessPlan{Ops: []string{"_bulk_docs {_deleted:true}: empty the database, keep its design documents"}}
}

// ExplainDrop describes the `iq data drop` op for a database and reports it
// supported — a CouchDB database is a removable container.
func ExplainDrop() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{"DELETE /{db}: remove the database and everything in it"}}, true
}

// ExplainDelete describes the `iq data delete` op for a database: fetch each key's
// _rev, then a _bulk_docs tombstone for the keys that have a live document.
func ExplainDelete() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{
		"_all_docs?keys=[...]: read each key's current _rev (a key with no document is Missing)",
		"_bulk_docs [{_id,_rev,_deleted:true}]: tombstone the named documents",
	}}, true
}

package mongo

import (
	"fmt"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// ExplainPlan describes the MongoDB calls this driver would make for a classified
// query, without connecting. A bounded query fetches the named documents by _id; a
// scan runs a find, pre-filtered by the pushed predicate when one compiles (else a
// full-collection scan). It reuses toFilter so the shown filter is exactly what the
// server would receive.
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
	return query.AccessPlan{Ops: []string{desc, mode}, Filter: filter}
}

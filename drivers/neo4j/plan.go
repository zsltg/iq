package neo4j

import (
	"fmt"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// ExplainPlan describes the Cypher a classified query would run, without connecting.
// A bounded query matches the named nodes by key; a scan pages the label with ORDER
// BY elementId (keyset), narrowed by a WHERE clause when the pushed predicate
// narrows, else a full label walk. It reuses the same translation the scan path
// uses so the shown filter is exactly what the server would receive.
func ExplainPlan(keys selector.KeySet, pred predicate.Node, unbounded bool) query.AccessPlan {
	if !keys.Scan {
		return query.AccessPlan{
			Ops:    []string{fmt.Sprintf("MATCH ... WHERE key IN $ids: fetch %d requested key(s)", len(keys.Keys))},
			Filter: map[string]any{"keys": keys.Keys},
		}
	}

	var filter map[string]any
	desc := "MATCH (n:<label>): full-label scan (every node read, filtered client-side)"
	if pred != nil {
		b := &cypherBuilder{params: map[string]any{}, variable: "n"}
		if where, narrowing := b.translate(pred); narrowing {
			filter = map[string]any{"where": where}
			desc = "MATCH (n:<label>) WHERE <predicate>: server-side pre-filter, then the full jq re-runs client-side"
		}
	}
	mode := fmt.Sprintf("pages streamed in batches of %d (keyset by elementId)", scanBatch)
	if unbounded || !keys.Streamable {
		mode = "whole result materialized in memory"
	}
	return query.AccessPlan{Ops: []string{desc, mode}, Filter: filter}
}

// ExplainWrite describes, without connecting, the write a copy into this label would
// make: a MERGE on the key property that sets the node's properties, or a
// guarded CREATE that skips an existing key.
func ExplainWrite(mode query.WriteMode) query.AccessPlan {
	op := fmt.Sprintf("MERGE (n:<label> {<key>}) SET n += props: replace-upsert by key, batches of %d", scanBatch)
	if mode == query.InsertOnly {
		op = fmt.Sprintf("MERGE ... ON CREATE SET / ON MATCH skip: insert new keys, skip existing, batches of %d", scanBatch)
	}
	return query.AccessPlan{Ops: []string{op}}
}

// ExplainClear describes the `iq data clear` op for a label: detach-delete every
// node carrying it, removing the nodes and any relationships they hold.
func ExplainClear() query.AccessPlan {
	return query.AccessPlan{Ops: []string{"MATCH (n:<label>) DETACH DELETE n: empty the label (nodes and their relationships)"}}
}

// ExplainDrop describes the `iq data drop` op and reports it unsupported: a label is
// not a droppable container in Neo4j (it exists only while some node carries it), so
// there is nothing to drop — clearing the label is the closest operation.
func ExplainDrop() (query.AccessPlan, bool) {
	return query.AccessPlan{}, false
}

// ExplainDelete describes the `iq data delete` op for a label: resolve each key to
// its node (by elementId or the ?key= property), then DETACH DELETE the matches.
func ExplainDelete() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{
		"MATCH (n:<label>) WHERE elementId(n) IN $ids (or n.<key> IN $ids): resolve the keys to nodes",
		"MATCH ... WHERE elementId(n) IN $eids DETACH DELETE n: remove the matched nodes and their relationships",
	}}, true
}

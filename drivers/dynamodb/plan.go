package dynamodb

import (
	"fmt"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// ExplainPlan describes the DynamoDB calls this driver would run for a classified
// query, without connecting. A bounded query reads the named items with BatchGetItem;
// a scan runs a Scan, pre-filtered by the pushed predicate's FilterExpression when one
// compiles, else a full-table scan. It reuses the same compiler as ScanFiltered (via
// its readable display form) so the shown filter is exactly what the scan would issue.
func ExplainPlan(keys selector.KeySet, pred predicate.Node, unbounded bool) query.AccessPlan {
	if !keys.Scan {
		return query.AccessPlan{
			Ops: []string{fmt.Sprintf("BatchGetItem: fetch %d requested key(s)", len(keys.Keys))},
		}
	}

	desc := "Scan: full-table scan (every item read, filtered client-side)"
	var filter map[string]any
	if f, ok := compile(pred); ok && f.display != "" {
		desc = "Scan with FilterExpression " + f.display + ": server-side pre-filter, then the full jq re-runs client-side"
		filter = map[string]any{"filter": f.display}
	}
	mode := fmt.Sprintf("cursor streamed in pages of %d", scanBatch)
	if unbounded || !keys.Streamable {
		mode = "whole result materialized in memory"
	}
	return query.AccessPlan{Ops: []string{desc, mode}, Filter: filter}
}

// ExplainWrite describes, without connecting, the write a copy into this table would
// make: a plain PutItem upsert by primary key, or a PutItem with an
// attribute_not_exists condition that skips existing keys.
func ExplainWrite(mode query.WriteMode) query.AccessPlan {
	op := "PutItem: upsert by primary key"
	if mode == query.InsertOnly {
		op = "PutItem with attribute_not_exists condition: insert new primary keys, skip existing"
	}
	return query.AccessPlan{Ops: []string{op}}
}

// ExplainClear describes the `iq data clear` op for a table: DynamoDB has no TRUNCATE,
// so it scans every item and deletes it, keeping the table definition.
func ExplainClear() query.AccessPlan {
	return query.AccessPlan{Ops: []string{
		"Scan + BatchWriteItem delete-all: empty the table, keep its schema " +
			"(consumes read and write capacity proportional to the item count)",
	}}
}

// ExplainDrop describes the `iq data drop` op for a table and reports it supported — a
// DynamoDB table is a removable container.
func ExplainDrop() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{"DeleteTable: remove the table and its data"}}, true
}

// ExplainDelete describes the `iq data delete` op for a table: BatchWriteItem
// delete by key, with a pre-read for the present-vs-absent count (BatchWriteItem is
// silent on it).
func ExplainDelete() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{
		"BatchGetItem pre-read the keys (accounting only, for present-vs-absent)",
		"BatchWriteItem DeleteRequest, batches of 25: remove the named keys by primary key",
	}}, true
}

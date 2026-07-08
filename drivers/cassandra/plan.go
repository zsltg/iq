package cassandra

import (
	"fmt"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// ExplainPlan describes the Cassandra statements this driver would run for a
// classified query, without connecting. A bounded query reads the named rows by
// primary key; a scan runs a SELECT, pre-filtered by the pushed predicate (with
// ALLOW FILTERING) when one compiles, else a full-table scan. It reuses toCQL so the
// shown WHERE is exactly what ScanFiltered would issue.
func ExplainPlan(keys selector.KeySet, pred predicate.Node, unbounded bool) query.AccessPlan {
	if !keys.Scan {
		return query.AccessPlan{
			Ops: []string{fmt.Sprintf("SELECT by primary key: fetch %d requested key(s)", len(keys.Keys))},
		}
	}

	where, _, ok := toCQL(nil, pred)
	desc := "SELECT *: full-table scan (every row read, filtered client-side)"
	var filter map[string]any
	if ok && where != "" {
		desc = "SELECT * WHERE " + where + " ALLOW FILTERING: server-side pre-filter, then the full jq re-runs client-side"
		filter = map[string]any{"where": where}
	}
	mode := fmt.Sprintf("cursor streamed in pages of %d", scanBatch)
	if unbounded || !keys.Streamable {
		mode = "whole result materialized in memory"
	}
	return query.AccessPlan{Ops: []string{desc, mode}, Filter: filter}
}

// ExplainWrite describes, without connecting, the write a copy into this table would
// make: a plain INSERT upsert by primary key, or an INSERT ... IF NOT EXISTS that
// skips existing keys.
func ExplainWrite(mode query.WriteMode) query.AccessPlan {
	op := "INSERT: upsert by primary key"
	if mode == query.InsertOnly {
		op = "INSERT ... IF NOT EXISTS: insert new primary keys, skip existing"
	}
	return query.AccessPlan{Ops: []string{op}}
}

// ExplainClear describes the `iq data clear` op for a table.
func ExplainClear() query.AccessPlan {
	return query.AccessPlan{Ops: []string{"TRUNCATE: empty the table, keep its schema"}}
}

// ExplainDrop describes the `iq data drop` op for a table and reports it supported —
// a Cassandra table is a removable container.
func ExplainDrop() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{"DROP TABLE: remove the table and its data"}}, true
}

// ExplainDelete describes the `iq data delete` op for a table: a per-key DELETE by
// primary key, with a pre-read for the present-vs-absent count (CQL is silent on it).
func ExplainDelete() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{
		"pre-read the keys (accounting only, for present-vs-absent)",
		"DELETE FROM <table> WHERE <pk> = ? per key: remove the named rows",
	}}, true
}

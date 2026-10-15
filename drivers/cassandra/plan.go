package cassandra

import (
	"fmt"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// ExplainPlan describes the Cassandra statements that this driver will run for a
// classified query, without connecting. A bounded query reads the named rows by
// primary key. A scan runs a SELECT. The pushed predicate pre-filters the SELECT
// (with ALLOW FILTERING) when one compiles. Otherwise the scan reads the full
// table. It reuses toCQL so the shown WHERE is exactly what ScanFiltered issues.
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
	server := query.ServerStage("", false)
	if filter != nil {
		server = query.StagePlan{
			State: query.StageConditional,
			Scope: "cql where",
			Note:  "each referenced column exists and its type accepts the literal",
		}
	}
	return query.AccessPlan{Ops: []string{desc, mode}, Filter: filter, Server: server}
}

// ExplainWrite describes, without connecting, the write that a copy into this
// table will make: a plain INSERT upsert by primary key, or an INSERT ... IF NOT
// EXISTS that skips existing keys.
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

// ExplainDrop describes the `iq data drop` op for a table and reports it
// supported. A Cassandra table is a removable container.
func ExplainDrop() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{"DROP TABLE: remove the table and its data"}}, true
}

// ExplainDelete describes the `iq data delete` op for a table. It is a per-key
// DELETE by primary key. A pre-read gives the present-vs-absent count, because
// CQL is silent on it.
func ExplainDelete() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{
		"pre-read the keys (accounting only, for present-vs-absent)",
		"DELETE FROM <table> WHERE <pk> = ? per key: remove the named rows",
	}}, true
}

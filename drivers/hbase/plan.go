package hbase

import (
	"fmt"
	"strings"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// ExplainPlan describes the HBase RPCs this driver would run for a classified query,
// without connecting. A bounded query reads the named rows by key with point Gets; a
// scan runs a table Scan, pre-filtered by a server-side SingleColumnValueFilter when
// the predicate has pushable equalities, else a full-table scan. It reuses
// pushdownColumns so the shown filter columns are exactly what ScanFiltered would push.
func ExplainPlan(keys selector.KeySet, pred predicate.Node, unbounded bool) query.AccessPlan {
	if !keys.Scan {
		return query.AccessPlan{
			Ops: []string{fmt.Sprintf("Get by row key: fetch %d requested key(s)", len(keys.Keys))},
		}
	}

	desc := "Scan: full-table scan (every row read, filtered client-side)"
	var filterInfo map[string]any
	if cols, ok := pushdownColumns(pred); ok {
		desc = "Scan + SingleColumnValueFilter on " + strings.Join(cols, ", ") +
			": server-side pre-filter, then the full jq re-runs client-side"
		filterInfo = map[string]any{"columns": cols}
	}
	mode := fmt.Sprintf("cursor streamed in pages of %d", scanBatch)
	if unbounded || !keys.Streamable {
		mode = "whole result materialized in memory"
	}
	return query.AccessPlan{Ops: []string{desc, mode}, Filter: filterInfo}
}

// ExplainWrite describes, without connecting, the write a copy into this table would
// make: a plain Put upsert by row key, or a CheckAndPut that skips rows whose guard
// cell already exists.
func ExplainWrite(mode query.WriteMode) query.AccessPlan {
	op := "Put: upsert cells by row key"
	if mode == query.InsertOnly {
		op = "CheckAndPut: write new rows, skip a row whose first cell already exists"
	}
	return query.AccessPlan{Ops: []string{op}}
}

// ExplainClear describes the `iq data clear` op for a table: a key-only scan then a
// Delete per row, since HBase has no client-side TRUNCATE.
func ExplainClear() query.AccessPlan {
	return query.AccessPlan{Ops: []string{"Scan (key-only) + Delete per row: empty the table, keep its schema"}}
}

// ExplainDrop describes the `iq data drop` op for a table and reports it supported —
// an HBase table is a removable container, disabled then deleted.
func ExplainDrop() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{"DisableTable then DeleteTable: remove the table and its data"}}, true
}

// ExplainDelete describes the `iq data delete` op for a table: a whole-row Delete
// per key, with an existence-only pre-read for the present-vs-absent count (Del is
// silent on it).
func ExplainDelete() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{
		"exists-only Get per key (accounting only, for present-vs-absent)",
		"Delete (whole row) per key: remove the named rows",
	}}, true
}

package hbase

import (
	"fmt"
	"strings"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// Source names the table to plan for: the saved URI and the optional address that
// overrides its ?table=. Open takes the same two values.
type Source struct {
	URI     string
	Address string
}

// ExplainPlan describes the HBase RPCs this driver runs for a classified query,
// without connecting. A bounded query reads the named rows by key with point Gets.
// A scan runs a table Scan. When the predicate has pushable equalities, a
// server-side SingleColumnValueFilter pre-filters the scan. Otherwise it is a
// full-table scan. ExplainPlan parses the URI for the column types and runs
// toFilter, as ScanFiltered does, so the plan shows what the scan pushes. It parses
// the source as Open does, so an address wins over the saved ?table=. A source that
// does not parse is an error.
func ExplainPlan(src Source, keys selector.KeySet, pred predicate.Node, unbounded bool) (query.AccessPlan, error) {
	cc, err := parseURL(src.URI, src.Address)
	if err != nil {
		return query.AccessPlan{}, fmt.Errorf("explain hbase source: %w", err)
	}
	if !keys.Scan {
		return query.AccessPlan{
			Ops: []string{fmt.Sprintf("Get by row key: fetch %d requested key(s)", len(keys.Keys))},
		}, nil
	}

	desc := "Scan: full-table scan (every row read, filtered client-side)"
	var filterInfo map[string]any
	if _, ok := toFilter(cc.types, pred); ok {
		cols := pushedColumns(cc.types, pred)
		desc = "Scan + SingleColumnValueFilter on " + strings.Join(cols, ", ") +
			": server-side pre-filter, then the full jq re-runs client-side"
		filterInfo = map[string]any{"columns": cols}
	}
	mode := fmt.Sprintf("cursor streamed in pages of %d", scanBatch)
	if unbounded || !keys.Streamable {
		mode = "whole result materialized in memory"
	}
	return query.AccessPlan{
		Ops:    []string{desc, mode},
		Filter: filterInfo,
		Server: query.ServerStage("single column value filter", filterInfo != nil),
	}, nil
}

// ExplainWrite describes, without connecting, the write that a copy into this
// table makes. It is a plain Put upsert by row key, or a CheckAndPut that skips
// rows whose guard cell already exists.
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

// ExplainDrop describes the `iq data drop` op for a table and reports it
// supported, because an HBase table is a removable container, disabled then
// deleted.
func ExplainDrop() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{"DisableTable then DeleteTable: remove the table and its data"}}, true
}

// ExplainDelete describes the `iq data delete` op for a table. It is a whole-row
// Delete per key, with an existence-only pre-read for the present-vs-absent count
// (Del is silent on it).
func ExplainDelete() (query.AccessPlan, bool) {
	return query.AccessPlan{Ops: []string{
		"exists-only Get per key (accounting only, for present-vs-absent)",
		"Delete (whole row) per key: remove the named rows",
	}}, true
}

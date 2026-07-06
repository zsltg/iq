package hbase

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/tsuna/gohbase/filter"
	"github.com/tsuna/gohbase/hrpc"

	"github.com/zsltg/iq/internal/query"
)

// Put writes a batch of records into the table, keyed by row key. Upsert issues a
// plain Put, which HBase applies as an idempotent overwrite of the named cells;
// InsertOnly issues an atomic CheckAndPut guarded on the absence of the row's first
// cell (lexicographically smallest family:qualifier), counting a row whose guard cell
// already exists as a skip. HBase has no whole-row "insert if absent" primitive — the
// check is per-cell — so a row that exists but lacks that specific guard cell is still
// written; document your rows carry a stable first column for exact insert-only
// semantics. Every value is encoded through its column's declared type; a value that
// cannot be represented, or a record whose value is not a row object, is a returned
// error, never a silent drop. HBase cannot cheaply distinguish an insert from an
// overwrite on Upsert, so an upsert counts every written row as Written.
func (s *Store) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	if s.table == "" {
		return query.WriteStat{}, errNoTable
	}
	var stat query.WriteStat
	for _, r := range batch {
		values, guardFamily, guardQualifier, err := s.columnsFor(r)
		if err != nil {
			return query.WriteStat{}, err
		}
		rk, err := encodeRowKey(s.rowkeyType, r.Key)
		if err != nil {
			return query.WriteStat{}, err
		}
		s.traceOp("put %s", s.table)
		req, err := hrpc.NewPut(ctx, []byte(s.table), rk, values)
		if err != nil {
			return query.WriteStat{}, fmt.Errorf("hbase put: %w", err)
		}
		if mode == query.InsertOnly {
			applied, err := s.client.CheckAndPut(req, guardFamily, guardQualifier, nil)
			if err != nil {
				return query.WriteStat{}, fmt.Errorf("hbase check-and-put: %w", err)
			}
			if applied {
				stat.Written++
			} else {
				stat.Skipped++
			}
			continue
		}
		if _, err := s.client.Put(req); err != nil {
			return query.WriteStat{}, fmt.Errorf("hbase put: %w", err)
		}
		stat.Written++
	}
	return stat, nil
}

// columnsFor builds the family→qualifier→value map to Put for a record, plus the
// guard cell (the lexicographically smallest family:qualifier) an insert-only write
// checks for absence. The record value must be a nested {family: {qualifier: value}}
// object; each cell is encoded through its column's declared type. An empty row is an
// error, since HBase has no empty rows and there would be no guard cell.
func (s *Store) columnsFor(r query.Record) (values map[string]map[string][]byte, guardFamily, guardQualifier string, err error) {
	obj, ok := r.Value.(map[string]any)
	if !ok {
		return nil, "", "", fmt.Errorf("hbase: record value must be a {family: {qualifier: value}} object, got %T", r.Value)
	}
	values = make(map[string]map[string][]byte, len(obj))
	for family, cols := range obj {
		colObj, ok := cols.(map[string]any)
		if !ok {
			return nil, "", "", fmt.Errorf("hbase: family %q must map qualifiers to values, got %T", family, cols)
		}
		qualifiers := make(map[string][]byte, len(colObj))
		for qualifier, v := range colObj {
			b, err := encodeCell(colTypeFor(s.types, family, qualifier), v)
			if err != nil {
				return nil, "", "", err
			}
			qualifiers[qualifier] = b
		}
		if len(qualifiers) > 0 {
			values[family] = qualifiers
		}
	}
	guardFamily, guardQualifier, ok = guardCell(values)
	if !ok {
		return nil, "", "", fmt.Errorf("hbase: record %q has no cells to write", r.Key)
	}
	return values, guardFamily, guardQualifier, nil
}

// guardCell returns the lexicographically smallest family:qualifier in a values map,
// the deterministic cell an insert-only CheckAndPut guards on. It reports false when
// the map is empty.
func guardCell(values map[string]map[string][]byte) (family, qualifier string, ok bool) {
	families := make([]string, 0, len(values))
	for f := range values {
		families = append(families, f)
	}
	sort.Strings(families)
	for _, f := range families {
		quals := make([]string, 0, len(values[f]))
		for q := range values[f] {
			quals = append(quals, q)
		}
		if len(quals) == 0 {
			continue
		}
		sort.Strings(quals)
		return f, quals[0], true
	}
	return "", "", false
}

// Clear empties the table, keeping its schema (the `iq data clear` semantics). HBase
// has no client-side TRUNCATE, so it scans every row key-only and deletes each row,
// bounded by ctx. It is not atomic: a failure partway leaves the already-deleted rows
// gone, which the WriteStat-style honesty of the write path accepts.
func (s *Store) Clear(ctx context.Context) error {
	if s.table == "" {
		return errNoTable
	}
	s.traceOp("clear %s", s.table)
	req, err := hrpc.NewScanStr(ctx, s.table, hrpc.Filters(filter.NewKeyOnlyFilter(false)))
	if err != nil {
		return fmt.Errorf("hbase clear: %w", err)
	}
	scanner := s.client.Scan(req)
	for {
		res, err := scanner.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = scanner.Close()
			return fmt.Errorf("hbase clear scan: %w", err)
		}
		if len(res.Cells) == 0 {
			continue
		}
		del, err := hrpc.NewDel(ctx, []byte(s.table), res.Cells[0].Row, nil)
		if err != nil {
			_ = scanner.Close()
			return fmt.Errorf("hbase clear: %w", err)
		}
		if _, err := s.client.Delete(del); err != nil {
			_ = scanner.Close()
			return fmt.Errorf("hbase clear delete: %w", err)
		}
	}
	return nil
}

// Drop removes the table entirely — its rows and schema (the `iq data drop`
// semantics). HBase requires a table be disabled before it is deleted, so this
// disables then deletes, bounded by ctx.
func (s *Store) Drop(ctx context.Context) error {
	if s.table == "" {
		return errNoTable
	}
	s.traceOp("drop %s", s.table)
	if err := s.admin.DisableTable(hrpc.NewDisableTable(ctx, []byte(s.table))); err != nil {
		return fmt.Errorf("hbase disable table: %w", err)
	}
	if err := s.admin.DeleteTable(hrpc.NewDeleteTable(ctx, []byte(s.table))); err != nil {
		return fmt.Errorf("hbase delete table: %w", err)
	}
	return nil
}

// TypedScan streams the whole table as typed records, reusing the ScanBatches pager.
// Every item is a row, so the type tag is "row"; the key is the row key and the value
// is the nested row object.
func (s *Store) TypedScan(ctx context.Context, fn func(batch []query.Record) error) error {
	return s.ScanBatches(ctx, func(batch map[string]any) error {
		recs := make([]query.Record, 0, len(batch))
		for k, v := range batch {
			recs = append(recs, query.Record{Key: k, Type: "row", Value: v})
		}
		return fn(recs)
	})
}

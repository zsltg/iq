package cassandra

import (
	"context"
	"fmt"
	"strings"

	"github.com/zsltg/iq/internal/query"
)

// Put writes a batch of records into the table, keyed by primary key. Upsert issues
// a plain INSERT, which Cassandra applies as an idempotent upsert; InsertOnly issues
// INSERT ... IF NOT EXISTS (a lightweight transaction) and counts a row whose key
// already exists as a skip. Every value is bound as a parameter — never string-built
// — and coerced to its column's type; a value that cannot be represented, an unknown
// column, or a record missing a primary-key column is a returned error, never a
// silent drop. Cassandra cannot cheaply tell an insert from an overwrite, so an
// upsert counts every written row as Written, never Overwritten.
func (s *Store) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	if s.meta == nil {
		return query.WriteStat{}, errNoTable
	}
	var stat query.WriteStat
	for _, r := range batch {
		cols, vals, err := s.columnsFor(r)
		if err != nil {
			return query.WriteStat{}, err
		}
		names := make([]string, len(cols))
		placeholders := make([]string, len(cols))
		for i, c := range cols {
			names[i] = quoteIdent(c)
			placeholders[i] = "?"
		}
		cql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
			s.tableRef(), strings.Join(names, ", "), strings.Join(placeholders, ", "))
		if mode == query.InsertOnly {
			applied, err := s.session.Query(cql+" IF NOT EXISTS", vals...).MapScanCASContext(ctx, map[string]any{})
			if err != nil {
				return query.WriteStat{}, fmt.Errorf("cassandra insert: %w", err)
			}
			if applied {
				stat.Written++
			} else {
				stat.Skipped++
			}
			continue
		}
		if err := s.session.Query(cql, vals...).ExecContext(ctx); err != nil {
			return query.WriteStat{}, fmt.Errorf("cassandra insert: %w", err)
		}
		stat.Written++
	}
	return stat, nil
}

// columnsFor builds the column names and bound values to INSERT for a record. The
// primary-key columns come from the record key (authoritative, already typed); the
// remaining columns come from the value object, each coerced to its column type. A
// keyless record must carry its primary-key columns in the object. An unknown column
// or a missing primary-key column is an error.
func (s *Store) columnsFor(r query.Record) ([]string, []any, error) {
	obj, ok := r.Value.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("cassandra: record value must be a row object, got %T", r.Value)
	}
	values := make(map[string]any, len(obj))
	fromKey := make(map[string]bool)
	if r.Key != "" {
		pkVals, err := decodeKey(s.meta, r.Key)
		if err != nil {
			return nil, nil, err
		}
		for i, c := range primaryKeyColumns(s.meta) {
			values[c.Name] = pkVals[i]
			fromKey[c.Name] = true
		}
	}
	for name, v := range obj {
		if fromKey[name] {
			// The key is the authoritative source of a primary-key column.
			continue
		}
		col, ok := s.meta.Columns[name]
		if !ok {
			return nil, nil, fmt.Errorf("cassandra: unknown column %q in table %q", name, s.table)
		}
		bv, err := bindValue(col.Type, v)
		if err != nil {
			return nil, nil, err
		}
		values[name] = bv
	}
	for _, c := range primaryKeyColumns(s.meta) {
		if _, ok := values[c.Name]; !ok {
			return nil, nil, fmt.Errorf("cassandra: record missing primary-key column %q", c.Name)
		}
	}
	cols := make([]string, 0, len(values))
	vals := make([]any, 0, len(values))
	for name, v := range values {
		cols = append(cols, name)
		vals = append(vals, v)
	}
	return cols, vals, nil
}

// Clear empties the table, keeping its schema (the `iq data clear` semantics for a
// wide-column store), via TRUNCATE.
func (s *Store) Clear(ctx context.Context) error {
	if s.meta == nil {
		return errNoTable
	}
	if err := s.session.Query("TRUNCATE " + s.tableRef()).ExecContext(ctx); err != nil {
		return fmt.Errorf("cassandra truncate: %w", err)
	}
	return nil
}

// Drop removes the table entirely — its rows and schema (the `iq data drop`
// semantics). The Store implements Dropper because a Cassandra table is a removable
// container, like a MongoDB collection.
func (s *Store) Drop(ctx context.Context) error {
	if s.meta == nil {
		return errNoTable
	}
	if err := s.session.Query("DROP TABLE " + s.tableRef()).ExecContext(ctx); err != nil {
		return fmt.Errorf("cassandra drop: %w", err)
	}
	return nil
}

// TypedScan streams the whole table as typed records, reusing the ScanBatches pager.
// Every item is a row, so the type tag is "row"; the key is the encoded primary key
// and the value is the normalized row.
func (s *Store) TypedScan(ctx context.Context, fn func(batch []query.Record) error) error {
	return s.ScanBatches(ctx, func(batch map[string]any) error {
		recs := make([]query.Record, 0, len(batch))
		for k, v := range batch {
			recs = append(recs, query.Record{Key: k, Type: "row", Value: v})
		}
		return fn(recs)
	})
}

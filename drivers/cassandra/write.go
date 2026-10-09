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
// silent drop. Cassandra's INSERT cannot tell an insert from an overwrite, so an
// upsert pre-reads which keys already exist and counts those as Overwritten.
func (s *Store) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	if s.meta == nil {
		return query.WriteStat{}, errNoTable
	}
	if mode == query.InsertOnly {
		return s.putInsertOnly(ctx, batch)
	}
	// An upsert pre-reads which keys already have a row so it can report an
	// overwrite; see existingKeys for the accounting-only, non-atomic caveat.
	var existing map[string]bool
	if mode == query.Upsert {
		var err error
		existing, err = s.existingKeys(ctx, batch)
		if err != nil {
			return query.WriteStat{}, err
		}
	}
	return s.putPlain(ctx, batch, existing)
}

// insertCQL returns the INSERT statement for the named columns of the table. It has one
// placeholder for each column, and it quotes every identifier.
func (s *Store) insertCQL(cols []string) string {
	names := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	for i, c := range cols {
		names[i] = quoteIdent(c)
		placeholders[i] = "?"
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		s.tableRef(), strings.Join(names, ", "), strings.Join(placeholders, ", "))
}

// putInsertOnly writes each record with INSERT ... IF NOT EXISTS (a lightweight
// transaction) and counts a row whose key already exists as Skipped.
func (s *Store) putInsertOnly(ctx context.Context, batch []query.Record) (query.WriteStat, error) {
	var stat query.WriteStat
	for _, r := range batch {
		cols, vals, err := s.columnsFor(r)
		if err != nil {
			return query.WriteStat{}, err
		}
		cql := s.insertCQL(cols) + " IF NOT EXISTS"
		applied, err := s.session.Query(cql, vals...).MapScanCASContext(ctx, map[string]any{})
		if err != nil {
			return query.WriteStat{}, fmt.Errorf("cassandra insert: %w", err)
		}
		if applied {
			stat.Written++
		} else {
			stat.Skipped++
		}
	}
	return stat, nil
}

// putPlain writes each record with a plain INSERT, which Cassandra applies as an upsert.
// A key in existing counts as Overwritten. A keyless record has no key to pre-read, so
// existing never holds it and it counts as Written (the couchdb precedent).
func (s *Store) putPlain(ctx context.Context, batch []query.Record, existing map[string]bool) (query.WriteStat, error) {
	var stat query.WriteStat
	for _, r := range batch {
		cols, vals, err := s.columnsFor(r)
		if err != nil {
			return query.WriteStat{}, err
		}
		if err := s.session.Query(s.insertCQL(cols), vals...).ExecContext(ctx); err != nil {
			return query.WriteStat{}, fmt.Errorf("cassandra insert: %w", err)
		}
		if existing[r.Key] {
			stat.Overwritten++
		} else {
			stat.Written++
		}
	}
	return stat, nil
}

// existingKeys returns which of a batch's non-empty keys already have a row, so an
// upsert can report them as Overwritten. It reuses Get — one WHERE ... IN for a
// single-column key, one point query per composite key — reading the rows only to
// learn which keys are present. The read is accounting only: it never changes what
// Put writes, and it is not atomic with the writes that follow, so a concurrent
// insert between the two can skew the count by one. The rows written are always
// correct.
func (s *Store) existingKeys(ctx context.Context, batch []query.Record) (map[string]bool, error) {
	keys := make([]string, 0, len(batch))
	for _, r := range batch {
		if r.Key != "" {
			keys = append(keys, r.Key)
		}
	}
	return s.existingKeySet(ctx, keys)
}

// existingKeySet returns which of the given keys already have a row, reusing Get.
// It is the accounting pre-read shared by Put's overwrite count and Delete's
// present-vs-absent count; the same non-atomic caveat applies.
func (s *Store) existingKeySet(ctx context.Context, keys []string) (map[string]bool, error) {
	rows, err := s.Get(ctx, keys)
	if err != nil {
		return nil, err
	}
	// Get omits a key with no row, so membership is the existence answer; a row
	// that normalizes to nil would still be a row.
	found := make(map[string]bool, len(rows))
	for k := range rows {
		found[k] = true
	}
	return found, nil
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
	values, err := s.keyColumnValues(r.Key)
	if err != nil {
		return nil, nil, err
	}
	if err := s.bindObject(obj, values); err != nil {
		return nil, nil, err
	}
	if err := s.requireKeyColumns(values); err != nil {
		return nil, nil, err
	}
	cols, vals := splitColumns(values)
	return cols, vals, nil
}

// keyColumnValues returns the primary-key column values that a record key carries,
// keyed by column name. An empty key carries none.
func (s *Store) keyColumnValues(key string) (map[string]any, error) {
	values := map[string]any{}
	if key == "" {
		return values, nil
	}
	pkVals, err := decodeKey(s.meta, key)
	if err != nil {
		return nil, err
	}
	for i, c := range primaryKeyColumns(s.meta) {
		values[c.Name] = pkVals[i]
	}
	return values, nil
}

// bindObject coerces each field of obj to its column type and adds it to values. A
// field whose name is already in values is a key column: the key is the authoritative
// source of a primary-key column, so the field is skipped.
func (s *Store) bindObject(obj, values map[string]any) error {
	for name, v := range obj {
		if _, isKey := values[name]; isKey {
			continue
		}
		col, ok := s.meta.Columns[name]
		if !ok {
			return fmt.Errorf("cassandra: unknown column %q in table %q", name, s.table)
		}
		bv, err := bindValue(col.Type, v)
		if err != nil {
			return err
		}
		values[name] = bv
	}
	return nil
}

// requireKeyColumns makes sure that values holds every primary-key column. It reports the
// first missing column in schema order.
func (s *Store) requireKeyColumns(values map[string]any) error {
	for _, c := range primaryKeyColumns(s.meta) {
		if _, ok := values[c.Name]; !ok {
			return fmt.Errorf("cassandra: record missing primary-key column %q", c.Name)
		}
	}
	return nil
}

// splitColumns returns the names and the values of m as two aligned slices.
func splitColumns(m map[string]any) ([]string, []any) {
	cols := make([]string, 0, len(m))
	vals := make([]any, 0, len(m))
	for name, v := range m {
		cols = append(cols, name)
		vals = append(vals, v)
	}
	return cols, vals
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

// Delete removes the named keys with one DELETE ... WHERE <pk> = ? per key, each key
// decoded to its primary-key bind values (a single-column key is the whole string, a
// composite key a JSON array in schema order). CQL is silent on whether a row existed,
// so a pre-read (existingKeySet, accounting only) supplies the present-vs-absent split;
// the DELETE runs for every requested key regardless, so the result is idempotent. Bind
// values are parameters, never string-built.
func (s *Store) Delete(ctx context.Context, keys []string) (query.DeleteStat, error) {
	if s.meta == nil {
		return query.DeleteStat{}, errNoTable
	}
	existing, err := s.existingKeySet(ctx, keys)
	if err != nil {
		return query.DeleteStat{}, err
	}
	pkCols := primaryKeyColumns(s.meta)
	conds := make([]string, len(pkCols))
	for i, c := range pkCols {
		conds[i] = quoteIdent(c.Name) + " = ?"
	}
	cql := fmt.Sprintf("DELETE FROM %s WHERE %s", s.tableRef(), strings.Join(conds, " AND "))
	var stat query.DeleteStat
	for _, k := range keys {
		pkVals, err := decodeKey(s.meta, k)
		if err != nil {
			return query.DeleteStat{}, err
		}
		if err := s.session.Query(cql, pkVals...).ExecContext(ctx); err != nil {
			return query.DeleteStat{}, fmt.Errorf("cassandra delete: %w", err)
		}
		if existing[k] {
			stat.Deleted++
		} else {
			stat.Missing++
		}
	}
	return stat, nil
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

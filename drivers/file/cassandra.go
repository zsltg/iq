package file

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	iqcass "github.com/zsltg/iq/drivers/cassandra"
	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// cassandraCSVSource streams a cqlsh `COPY … TO` CSV dump. A COPY dump carries every
// value as text and records neither column types nor the primary key, so the ?types=
// hint re-types the columns and the ?keys= hint names the key columns; each row is then
// decoded to the exact record a live scan yields (see drivers/cassandra), keyed by its
// primary key and tagged "row". Column names come from the header row, or from a
// ?columns= hint when the dump was exported without HEADER=TRUE.
func cassandraCSVSource(r io.Reader, size int, dec numfmt.DecimalMode, hints Hints) (query.RecordSource, error) {
	if hints.Keys == "" {
		return nil, errors.New("cassandra dump needs a key schema: add ?keys=col1[,col2] naming the primary-key columns")
	}
	keyCols := splitCols(hints.Keys)
	types, err := iqcass.ParseColumnTypes(hints.Types)
	if err != nil {
		return nil, err
	}
	var header []string
	if hints.Columns != "" {
		header = splitCols(hints.Columns)
	}
	return func(ctx context.Context, fn func(batch []query.Record) error) error {
		cr := csv.NewReader(r)
		cr.ReuseRecord = true
		cr.FieldsPerRecord = -1 // don't enforce a width here; recordForCSVRow validates against the header.
		if header == nil {
			h, err := cr.Read()
			if errors.Is(err, io.EOF) {
				return nil // empty dump.
			}
			if err != nil {
				return fmt.Errorf("read cassandra csv header: %w", err)
			}
			header = append([]string(nil), h...) // copy: ReuseRecord reuses the row slice.
		}
		page := make([]query.Record, 0, size)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			fields, err := cr.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("read cassandra csv row: %w", err)
			}
			rec, err := recordForCSVRow(header, fields, keyCols, types, dec)
			if err != nil {
				return err
			}
			page = append(page, rec)
			if len(page) >= size {
				if err := fn(page); err != nil {
					return err
				}
				page = page[:0]
			}
		}
		if len(page) > 0 {
			return fn(page)
		}
		return nil
	}, nil
}

// recordForCSVRow turns one CSV row into the record a live Cassandra scan yields. Each
// field is re-typed to the value gocql would bind for its column (text when the column
// has no ?types= entry), so the key and the normalized value match a live scan exactly.
// An empty field is the CQL null cqlsh writes by default.
func recordForCSVRow(header, fields, keyCols []string, types map[string]gocql.Type, dec numfmt.DecimalMode) (query.Record, error) {
	if len(fields) != len(header) {
		return query.Record{}, fmt.Errorf("cassandra csv row has %d field(s), header has %d", len(fields), len(header))
	}
	typed := make(map[string]any, len(header))
	for i, name := range header {
		field := fields[i]
		if field == "" {
			typed[name] = nil // cqlsh's default null marker is an empty field.
			continue
		}
		ct, ok := types[name]
		if !ok {
			ct = gocql.TypeText // an untyped column is read as text.
		}
		v, err := iqcass.BindString(ct, field)
		if err != nil {
			return query.Record{}, fmt.Errorf("cassandra csv column %q: %w", name, err)
		}
		typed[name] = v
	}
	val := make(map[string]any, len(typed))
	for name, v := range typed {
		val[name] = iqcass.Normalize(v, dec)
	}
	return query.Record{Key: iqcass.KeyOfColumns(keyCols, typed), Type: "row", Value: val}, nil
}

// splitCols splits a comma-separated column-list hint (?keys= or ?columns=), trimming
// surrounding whitespace from each name.
func splitCols(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if name := strings.TrimSpace(p); name != "" {
			out = append(out, name)
		}
	}
	return out
}

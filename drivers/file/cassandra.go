package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	iqcass "github.com/zsltg/iq/drivers/cassandra"
	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// csvLayout describes the columns of a cqlsh CSV dump: the column names in row
// order, the primary-key columns, and the CQL type of each typed column.
type csvLayout struct {
	header  []string
	keyCols []string
	types   map[string]gocql.Type
}

// cassandraCSVSource streams a cqlsh `COPY … TO` CSV dump. A COPY dump carries every
// value as text and records neither column types nor the primary key. The ?types=
// hint re-types the columns and the ?keys= hint names the key columns. Each row then
// decodes to the record that a live scan yields (see drivers/cassandra), keyed by its
// primary key and tagged "row". Column names come from the header row, or from a
// ?columns= hint if the dump has no HEADER=TRUE.
func cassandraCSVSource(r io.Reader, size int, dec numfmt.DecimalMode, hints Hints) (query.RecordSource, error) {
	return cassandraCSVSourceCap(capped{r, query.MaxRecordBytes}, size, dec, hints)
}

// cassandraCSVSourceCap is cassandraCSVSource with a limit on the size of one
// CSV record.
func cassandraCSVSourceCap(src capped, size int, dec numfmt.DecimalMode, hints Hints) (query.RecordSource, error) {
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
		cr := src.csvReader()
		cr.ReuseRecord = true
		cr.FieldsPerRecord = -1 // don't enforce a width here; recordForCSVRow validates against the header.
		n := 1                  // the CSV record number that the next read returns.
		if header == nil {
			h, err := readCSVHeader(cr, src.limit)
			if err != nil {
				return err
			}
			header = h // nil for an empty dump. The loop below then ends at once.
			n++
		}
		pg := newRecordPager(size, fn)
		for ; ; n++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			fields, err := cr.Read()
			if err == io.EOF { //nolint:errorlint // the decoder returns a bare io.EOF only at a clean end; a wrapped one is a reader failure.
				break
			}
			if errors.Is(err, errValueTooLarge) {
				return tooLarge("cassandra csv", n, src.limit)
			}
			if err != nil {
				return fmt.Errorf("read cassandra csv row: %w", err)
			}
			rec, err := recordForCSVRow(ctx, csvLayout{header, keyCols, types}, fields, dec)
			if err != nil {
				return err
			}
			if err := pg.add(rec); err != nil {
				return err
			}
		}
		return pg.flush()
	}, nil
}

// recordForCSVRow turns one CSV row into the record that a live Cassandra scan
// yields. Each field is re-typed to the value gocql binds for its column (text
// when the column has no ?types= entry). So the key and the normalized value
// match a live scan exactly. An empty field is the CQL null that cqlsh writes by
// default.
func recordForCSVRow(ctx context.Context, layout csvLayout, fields []string, dec numfmt.DecimalMode) (query.Record, error) {
	header := layout.header
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
		ct, ok := layout.types[name]
		if !ok {
			ct = gocql.TypeText // an untyped column is read as text.
		}
		v, err := iqcass.BindString(ct, field)
		if err != nil {
			return query.Record{}, fmt.Errorf("cassandra csv column %q: %w", name, err)
		}
		typed[name] = v
	}
	h := &numfmt.Hits{}
	val := make(map[string]any, len(typed))
	for name, v := range typed {
		h.Enter(name)
		val[name] = iqcass.Normalize(v, dec, h)
		h.Leave()
	}
	key := iqcass.KeyOfColumns(layout.keyCols, typed)
	query.NoteNonFinite(ctx, key, h.Fields...)
	return query.Record{Key: key, Type: "row", Value: val}, nil
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

// readCSVHeader reads the header row of a dump. It returns a nil header and a
// nil error for an empty dump.
func readCSVHeader(cr *csvRows, limit int64) ([]string, error) {
	h, err := cr.Read()
	if err == io.EOF { //nolint:errorlint // the decoder returns a bare io.EOF only at a clean end; a wrapped one is a reader failure.
		return nil, nil // empty dump.
	}
	if errors.Is(err, errValueTooLarge) {
		return nil, tooLarge("cassandra csv", 1, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("read cassandra csv header: %w", err)
	}
	return append([]string(nil), h...), nil // copy: ReuseRecord reuses the row slice.
}

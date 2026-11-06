package hbase

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/tsuna/gohbase/hrpc"

	"github.com/zsltg/iq/internal/query"
)

// The native form of this driver is query.FormHBaseRaw: one row as raw bytes in
// base64. The record key is the base64 of the row key. The value is
// {family: {base64(qualifier): base64(cell value)}}. The family stays text,
// because HBase family names are printable. The form is the only marker, so a
// cell has no tag. The form ignores ?types= and ?keytype=, because raw means raw.
// It keeps the latest version of each cell and no timestamp, as a normal scan does.

// errRawNoCells is the cause of a raw record that has no cell to write.
var errRawNoCells = errors.New("has no cells to write")

// NativeForm names the form that NativeScan emits.
func (s *Store) NativeForm() query.Form { return query.FormHBaseRaw }

// WritesForm names the native form that Put accepts besides normalized JSON.
func (s *Store) WritesForm() query.Form { return query.FormHBaseRaw }

// NativeScan streams the whole table as raw records, one page of pageSize rows at
// a time. It reads the same cells as ScanBatches, with the same row cell cap and
// the same --unbounded rule. ctx bounds the scan. It stops at the first error from
// fn or the region server.
func (s *Store) NativeScan(ctx context.Context, fn func(batch []query.Record) error) error {
	if s.table == "" {
		return errNoTable
	}
	s.traceOp("native scan %s", s.table)
	req, err := hrpc.NewScanStr(ctx, s.table, s.readOptions(ctx)...)
	if err != nil {
		return fmt.Errorf("hbase scan: %w", err)
	}
	page := make([]query.Record, 0, s.pageSize)
	err = eachRow(s.client.Scan(req), "hbase scan", func(res *hrpc.Result) (bool, error) {
		rec, err := s.rawRecord(ctx, res.Cells)
		if err != nil {
			return false, err
		}
		page = append(page, rec)
		if len(page) < s.pageSize {
			return false, nil
		}
		err = fn(page)
		page = make([]query.Record, 0, s.pageSize)
		return false, err
	})
	if err != nil {
		return err
	}
	// The final partial page runs after the scanner returned io.EOF.
	if len(page) > 0 {
		return fn(page)
	}
	return nil
}

// rawRecord builds the raw record of one row. It applies the row cell cap first.
func (s *Store) rawRecord(ctx context.Context, cells []*hrpc.Cell) (query.Record, error) {
	key := base64.StdEncoding.EncodeToString(cells[0].Row)
	if err := s.admitRow(ctx, key, cells); err != nil {
		return query.Record{}, err
	}
	return query.Record{Key: key, Type: "row", Form: query.FormHBaseRaw, Value: rawRow(cells)}, nil
}

// rawRow groups the cells of a row into {family: {base64(qualifier): base64(value)}}.
func rawRow(cells []*hrpc.Cell) map[string]any {
	row := make(map[string]any)
	for _, c := range cells {
		family := string(c.Family)
		fam, ok := row[family].(map[string]any)
		if !ok {
			fam = make(map[string]any)
			row[family] = fam
		}
		fam[base64.StdEncoding.EncodeToString(c.Qualifier)] = base64.StdEncoding.EncodeToString(c.Value)
	}
	return row
}

// rawWrite decodes a raw record into a row write. It checks the cells before the
// row key, as the normalized path does. Every error names the record key.
func rawWrite(r query.Record) (rowWrite, error) {
	values, err := rawCells(r)
	if err != nil {
		return rowWrite{}, err
	}
	key, err := decodeRawBase64(r.Key)
	if err != nil {
		return rowWrite{}, fmt.Errorf("hbase: record %q: the key is not valid base64: %w", r.Key, err)
	}
	if len(key) == 0 {
		return rowWrite{}, fmt.Errorf("hbase: record %q: the row key is empty", r.Key)
	}
	guardFamily, guardQualifier, ok := guardCell(values)
	if !ok {
		return rowWrite{}, fmt.Errorf("hbase: record %q %w", r.Key, errRawNoCells)
	}
	return rowWrite{key: key, values: values, guardFamily: guardFamily, guardQualifier: guardQualifier}, nil
}

// rawCells decodes the {family: {qualifier: value}} object of a raw record. A
// family without a cell is dropped, as on the normalized path.
func rawCells(r query.Record) (map[string]map[string][]byte, error) {
	obj, ok := r.Value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("hbase: record %q: the value must be a {family: {qualifier: value}} object, got %T", r.Key, r.Value)
	}
	values := make(map[string]map[string][]byte, len(obj))
	for family, cols := range obj {
		qualifiers, err := rawFamily(r.Key, family, cols)
		if err != nil {
			return nil, err
		}
		if len(qualifiers) > 0 {
			values[family] = qualifiers
		}
	}
	return values, nil
}

// rawFamily decodes the cells of one family. The map key is the raw qualifier
// bytes held in a Go string.
func rawFamily(key, family string, cols any) (map[string][]byte, error) {
	if family == "" {
		return nil, fmt.Errorf("hbase: record %q: a family name is empty", key)
	}
	obj, ok := cols.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("hbase: record %q: family %q must map qualifiers to values, got %T", key, family, cols)
	}
	qualifiers := make(map[string][]byte, len(obj))
	for q, v := range obj {
		qb, err := decodeRawBase64(q)
		if err != nil {
			return nil, fmt.Errorf("hbase: record %q: family %q: qualifier %q is not valid base64: %w", key, family, q, err)
		}
		vb, err := rawValue64(v)
		if err != nil {
			return nil, fmt.Errorf("hbase: record %q: cell %q: %w", key, family+":"+q, err)
		}
		qualifiers[string(qb)] = vb
	}
	return qualifiers, nil
}

// rawValue64 decodes the value of one raw cell. It must be a base64 string. The
// error never holds the value, because a cell can hold private data.
func rawValue64(v any) ([]byte, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("the value must be a base64 string, got %T", v)
	}
	b, err := decodeRawBase64(s)
	if err != nil {
		return nil, fmt.Errorf("the value is not valid base64: %w", err)
	}
	return b, nil
}

// decodeRawBase64 decodes standard base64 strictly. It refuses a missing pad,
// non-zero trailing bits, a line break, and any other text that the canonical
// encoding of the decoded bytes would not give back.
func decodeRawBase64(s string) ([]byte, error) {
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, err
	}
	if base64.StdEncoding.EncodeToString(b) != s {
		return nil, errors.New("not in canonical form")
	}
	return b, nil
}

package hbase

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/tsuna/gohbase/hrpc"
)

// colType is how one HBase cell value (raw bytes) is presented to the filter and
// reversed on write. HBase stores no types, so the default is honest — text when
// the bytes are valid UTF-8, else base64 — and a caller who knows a column's
// encoding declares it (?types=cf:q=long) to get an exact, reversible mapping via
// the standard HBase Bytes layout.
type colType int

const (
	// ctAuto is the default for an undeclared column: decode valid UTF-8 as a bare
	// string, else a base64 string; encode a string as its UTF-8 bytes. It never
	// guesses a numeric type. A value read back as base64 (non-UTF-8 bytes) does not
	// round-trip through a write — declare the column as bytes for that.
	ctAuto colType = iota
	// ctText decodes bytes as a UTF-8 string and encodes a string as its UTF-8
	// bytes, unconditionally (no base64 fallback): the caller asserts the column is
	// text.
	ctText
	// ctBytes decodes bytes to a base64 string and encodes a base64 string back to
	// raw bytes, the lossless representation for binary columns.
	ctBytes
	// ctInt is a 4-byte big-endian two's-complement int32 (HBase Bytes.toBytes(int)).
	ctInt
	// ctLong is an 8-byte big-endian two's-complement int64 (Bytes.toBytes(long)).
	ctLong
	// ctDouble is an 8-byte IEEE-754 big-endian double (Bytes.toBytes(double)).
	ctDouble
	// ctBool is a single byte, 0 or 1 (Bytes.toBytes(boolean)).
	ctBool
)

// typeMap declares the encoding of specific columns, keyed by "family:qualifier".
// A column absent from the map is ctAuto.
type typeMap map[string]colType

// cellKey is the typeMap key for a family and qualifier.
func cellKey(family, qualifier string) string {
	return family + ":" + qualifier
}

// parseColType resolves a declared type name to a colType. An unknown name is an
// error so a bad ?types= entry fails fast at connect rather than silently ignoring
// the declaration.
func parseColType(name string) (colType, error) {
	switch name {
	case "text", "string":
		return ctText, nil
	case "bytes", "binary", "blob":
		return ctBytes, nil
	case "int", "int32":
		return ctInt, nil
	case "long", "int64":
		return ctLong, nil
	case "double", "float", "float64":
		return ctDouble, nil
	case "bool", "boolean":
		return ctBool, nil
	default:
		return ctAuto, fmt.Errorf("hbase: unknown column type %q; want text, bytes, int, long, double, or bool", name)
	}
}

// decodeCell renders a raw cell value as a JSON-ready value under its declared
// type. A declared numeric type whose bytes are the wrong width falls back to the
// honest ctAuto rendering rather than aborting a scan, since a single malformed
// cell must not fail the whole query.
func decodeCell(t colType, b []byte) any {
	switch t {
	case ctText:
		return string(b)
	case ctBytes:
		return base64.StdEncoding.EncodeToString(b)
	case ctInt:
		if len(b) != 4 {
			return decodeAuto(b)
		}
		return int(int32(binary.BigEndian.Uint32(b))) //nolint:gosec // G115: decodes a stored 4-byte int column, deliberate 2's-complement.
	case ctLong:
		if len(b) != 8 {
			return decodeAuto(b)
		}
		return int(int64(binary.BigEndian.Uint64(b))) //nolint:gosec // G115: decodes a stored 8-byte long column, deliberate reinterpretation.
	case ctDouble:
		if len(b) != 8 {
			return decodeAuto(b)
		}
		return math.Float64frombits(binary.BigEndian.Uint64(b))
	case ctBool:
		if len(b) != 1 {
			return decodeAuto(b)
		}
		return b[0] != 0
	default:
		return decodeAuto(b)
	}
}

// decodeAuto is the untyped rendering: valid UTF-8 becomes a bare string, anything
// else a base64 string.
func decodeAuto(b []byte) any {
	if utf8.Valid(b) {
		return string(b)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// encodeCell reverses decodeCell: it turns a JSON value back into the raw cell
// bytes its column expects. A value of the wrong shape for the declared type is a
// returned error, never a silent coercion, so a write never corrupts a typed
// column. An undeclared (ctAuto) column encodes a string as its UTF-8 bytes; a
// value read back as base64 does not round-trip through ctAuto (declare bytes).
func encodeCell(t colType, v any) ([]byte, error) {
	switch t {
	case ctAuto, ctText:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("hbase: expected a string value, got %T", v)
		}
		return []byte(s), nil
	case ctBytes:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("hbase: expected a base64 string for a bytes column, got %T", v)
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("hbase: value is not valid base64 for a bytes column: %w", err)
		}
		return b, nil
	case ctInt:
		n, err := toInt64(v)
		if err != nil {
			return nil, err
		}
		if n < math.MinInt32 || n > math.MaxInt32 {
			return nil, fmt.Errorf("hbase: value %d out of range for a 4-byte int column", n)
		}
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(int32(n))) //nolint:gosec // G115: 2's-complement encoding of the bounds-checked int32 above.
		return b, nil
	case ctLong:
		n, err := toInt64(v)
		if err != nil {
			return nil, err
		}
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, uint64(n)) //nolint:gosec // G115: encodes an int64 as 8 bytes; the read path reverses it exactly.
		return b, nil
	case ctDouble:
		f, err := toFloat64(v)
		if err != nil {
			return nil, err
		}
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, math.Float64bits(f))
		return b, nil
	case ctBool:
		bv, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("hbase: expected a bool value, got %T", v)
		}
		if bv {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	default:
		return nil, fmt.Errorf("hbase: unknown column type %d", t)
	}
}

// toInt64 coerces a JSON scalar to an int64 for an int/long column. It accepts the
// shapes a decoded JSON number can take (float64, the gojq int, json.Number, or a
// numeric string), and rejects a fractional float so a long column never silently
// truncates.
func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case float64:
		if n != math.Trunc(n) {
			return 0, fmt.Errorf("hbase: %v is not a whole number for an int/long column", n)
		}
		return int64(n), nil
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("hbase: %q is not an integer for an int/long column: %w", n, err)
		}
		return i, nil
	default:
		return 0, fmt.Errorf("hbase: expected a number for an int/long column, got %T", v)
	}
}

// toFloat64 coerces a JSON scalar to a float64 for a double column.
func toFloat64(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, fmt.Errorf("hbase: %q is not a number for a double column: %w", n, err)
		}
		return f, nil
	default:
		return 0, fmt.Errorf("hbase: expected a number for a double column, got %T", v)
	}
}

// rowFromCells groups a result's cells into the nested {family: {qualifier: value}}
// row the KV model exposes, decoding each cell under its declared type. The latest
// version of each column is used (the requests set MaxVersions(1)), so a family's
// qualifier maps to a single value.
func rowFromCells(cells []*hrpc.Cell, types typeMap) map[string]any {
	row := make(map[string]any)
	for _, c := range cells {
		family := string(c.Family)
		qualifier := string(c.Qualifier)
		fam, ok := row[family].(map[string]any)
		if !ok {
			fam = make(map[string]any)
			row[family] = fam
		}
		fam[qualifier] = decodeCell(colTypeFor(types, family, qualifier), c.Value)
	}
	return row
}

// colTypeFor returns the declared type of a column, or ctAuto when undeclared.
func colTypeFor(types typeMap, family, qualifier string) colType {
	if t, ok := types[cellKey(family, qualifier)]; ok {
		return t
	}
	return ctAuto
}

// rowKeyString renders a raw row key as the string the table is keyed by under the
// row-key type: valid UTF-8 as itself, else base64 (or as declared).
func rowKeyString(t colType, key []byte) string {
	v := decodeCell(t, key)
	if s, ok := v.(string); ok {
		return s
	}
	// A declared numeric row-key type decodes to a number; render it canonically so
	// the map key stays a string, reversible by encodeRowKey under the same type.
	return fmt.Sprintf("%v", v)
}

// encodeRowKey reverses rowKeyString: it turns the string map key back into the raw
// row-key bytes under the row-key type, so a bounded Get or a write addresses the
// same physical row that a scan surfaced.
func encodeRowKey(t colType, key string) ([]byte, error) {
	switch t {
	case ctAuto, ctText:
		return []byte(key), nil
	default:
		return encodeCell(t, rowKeyValue(t, key))
	}
}

// rowKeyValue coerces a string map key to the value shape encodeCell expects for a
// declared row-key type: a base64 string stays a string for ctBytes; a numeric key
// is parsed so encodeCell can pack it.
func rowKeyValue(t colType, key string) any {
	switch t {
	case ctBytes:
		return key
	case ctBool:
		return key == "true"
	default:
		return key
	}
}

package parquetout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
)

// Writer streams normalized values to Parquet. It buffers the first
// sampleBufferSize values to infer the schema, then writes one record batch per
// page so memory stays bounded to a single page after the sample drains. Add
// each value in order; Close writes the final page and the file footer. It is
// not safe for concurrent use.
type Writer struct {
	w       io.Writer
	mem     memory.Allocator
	sample  []any
	started bool

	plan *plan
	fw   *pqarrow.FileWriter
	rb   *array.RecordBuilder
	rows int
}

// NewWriter returns a Writer that streams Parquet to w. Parquet writes its
// footer last but only ever appends, so a plain io.Writer (no Seek) suffices.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w, mem: memory.NewGoAllocator()}
}

// Add encodes one value as a row. Until the sample fills it only buffers; the
// schema is inferred and the first page written once the buffer is full.
func (pw *Writer) Add(v any) error {
	if !pw.started {
		pw.sample = append(pw.sample, v)
		if len(pw.sample) >= sampleBufferSize {
			return pw.start()
		}
		return nil
	}
	return pw.encodeRow(v)
}

// Close writes any buffered sample and the final partial page, then closes the
// Parquet file. Calling it on a Writer that never saw sampleBufferSize values
// infers the schema from the partial (or empty) sample first, so a short or
// empty result still produces a valid file.
func (pw *Writer) Close() error {
	if !pw.started {
		if err := pw.start(); err != nil {
			return err
		}
	}
	if err := pw.flushPage(); err != nil {
		return err
	}
	if err := pw.fw.Close(); err != nil {
		return fmt.Errorf("close parquet: %w", err)
	}
	return nil
}

// start infers the schema from the buffered sample, opens the Parquet writer
// with the Arrow schema stored under ARROW:schema (WithStoreSchema) so exact
// types round-trip, then drains the sample through the encoder.
func (pw *Writer) start() error {
	p := inferPlan(pw.sample)
	fw, err := pqarrow.NewFileWriter(
		p.schema, pw.w,
		parquet.NewWriterProperties(),
		pqarrow.NewArrowWriterProperties(pqarrow.WithStoreSchema()),
	)
	if err != nil {
		return fmt.Errorf("open parquet: %w", err)
	}
	pw.plan = p
	pw.fw = fw
	pw.rb = array.NewRecordBuilder(pw.mem, p.schema)
	pw.started = true

	sample := pw.sample
	pw.sample = nil
	for _, v := range sample {
		if err := pw.encodeRow(v); err != nil {
			return err
		}
	}
	return nil
}

// encodeRow appends one value as a row and flushes the page when it is full.
// A single-value plan appends the whole value to the one column; otherwise each
// column reads its field from the value, which must be an object.
func (pw *Writer) encodeRow(v any) error {
	if pw.plan.singleValue {
		if err := appendVal(pw.rb.Field(0), pw.plan.columns[0].enc, v, pw.plan.columns[0].name); err != nil {
			return err
		}
	} else {
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("parquet: expected an object row but got %s; use --format jsonl for heterogeneous data", jsonTypeName(v))
		}
		for i, col := range pw.plan.columns {
			if err := appendVal(pw.rb.Field(i), col.enc, obj[col.name], col.name); err != nil {
				return err
			}
		}
	}
	pw.rows++
	if pw.rows >= pageBatchSize {
		return pw.flushPage()
	}
	return nil
}

// flushPage writes the accumulated rows as one record batch and resets the
// builder. An empty page is a no-op, so Close on an all-flushed stream writes no
// trailing batch.
func (pw *Writer) flushPage() error {
	if pw.rows == 0 {
		return nil
	}
	rec := pw.rb.NewRecordBatch()
	defer rec.Release()
	if err := pw.fw.Write(rec); err != nil {
		return fmt.Errorf("write parquet batch: %w", err)
	}
	pw.rows = 0
	return nil
}

// appendVal appends v to builder b following the encoder plan e. A nil value is
// a null in every column (Arrow's validity bitmap); a value that does not fit
// the inferred column type fails the export, naming the path — the exact-round-
// trip ethos, never a silent coercion.
func appendVal(b array.Builder, e *enc, v any, path string) error {
	if v == nil {
		b.AppendNull()
		return nil
	}
	switch e.kind {
	case encJSON:
		s, err := canonicalJSON(v)
		if err != nil {
			return fmt.Errorf("parquet: encode %q as json: %w", path, err)
		}
		// The arrow.json column is plain utf8 storage; append the canonical text.
		b.(*array.StringBuilder).Append(s)
	case encInt:
		n, ok := toInt64(v)
		if !ok {
			return mismatch(path, "int64", v)
		}
		b.(*array.Int64Builder).Append(n)
	case encFloat:
		f, ok := toFloat64(v)
		if !ok {
			return mismatch(path, "double", v)
		}
		b.(*array.Float64Builder).Append(f)
	case encBool:
		bv, ok := v.(bool)
		if !ok {
			return mismatch(path, "bool", v)
		}
		b.(*array.BooleanBuilder).Append(bv)
	case encString:
		s, ok := v.(string)
		if !ok {
			return mismatch(path, "utf8", v)
		}
		b.(*array.StringBuilder).Append(s)
	case encTimestamp:
		return appendTimestamp(b, v, path)
	case encDate:
		return appendDate(b, v, path)
	case encStruct:
		return appendStruct(b, e, v, path)
	case encList:
		return appendList(b, e, v, path)
	case encMap:
		return appendMap(b, e, v, path)
	default:
		return mismatch(path, "unknown", v)
	}
	return nil
}

// appendTimestamp parses an RFC3339Nano string into a nanosecond UTC timestamp.
// A non-string value or an unparseable timestamp fails the export rather than
// coercing to a wrong instant.
func appendTimestamp(b array.Builder, v any, path string) error {
	s, ok := v.(string)
	if !ok {
		return mismatch(path, "timestamp[ns, UTC]", v)
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("parquet: %q is not an RFC3339Nano timestamp for column %q; use --format jsonl for heterogeneous data", s, path)
	}
	b.(*array.TimestampBuilder).Append(arrow.Timestamp(t.UTC().UnixNano()))
	return nil
}

// appendDate parses a YYYY-MM-DD string into a date32 day count.
func appendDate(b array.Builder, v any, path string) error {
	s, ok := v.(string)
	if !ok {
		return mismatch(path, "date32", v)
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return fmt.Errorf("parquet: %q is not a YYYY-MM-DD date for column %q; use --format jsonl for heterogeneous data", s, path)
	}
	b.(*array.Date32Builder).Append(arrow.Date32FromTime(t))
	return nil
}

// appendStruct appends an object's fields to a struct builder, each field read
// by name (a missing field is a null in its column).
func appendStruct(b array.Builder, e *enc, v any, path string) error {
	m, ok := v.(map[string]any)
	if !ok {
		return mismatch(path, "struct", v)
	}
	sb := b.(*array.StructBuilder)
	sb.Append(true)
	for i, f := range e.fields {
		if err := appendVal(sb.FieldBuilder(i), f.enc, m[f.name], path+"."+f.name); err != nil {
			return err
		}
	}
	return nil
}

// appendList appends an array's elements to a list builder.
func appendList(b array.Builder, e *enc, v any, path string) error {
	arr, ok := v.([]any)
	if !ok {
		return mismatch(path, "list", v)
	}
	lb := b.(*array.ListBuilder)
	lb.Append(true)
	for _, el := range arr {
		if err := appendVal(lb.ValueBuilder(), e.elem, el, path+"[]"); err != nil {
			return err
		}
	}
	return nil
}

// appendMap appends an object's entries to a map builder, keys sorted for a
// deterministic layout. Keys are always utf8; the value follows the elem plan.
func appendMap(b array.Builder, e *enc, v any, path string) error {
	m, ok := v.(map[string]any)
	if !ok {
		return mismatch(path, "map", v)
	}
	mb := b.(*array.MapBuilder)
	mb.Append(true)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kb := mb.KeyBuilder().(*array.StringBuilder)
	for _, k := range keys {
		kb.Append(k)
		if err := appendVal(mb.ItemBuilder(), e.elem, m[k], path+"{}"); err != nil {
			return err
		}
	}
	return nil
}

// mismatch reports a value that does not fit its inferred column type, naming
// the column and the type and pointing at the lossless fallback format.
func mismatch(path, arrowType string, v any) error {
	return fmt.Errorf("parquet: value of type %s at column %q does not fit inferred type %s; use --format jsonl for heterogeneous data",
		jsonTypeName(v), path, arrowType)
}

// toInt64 converts an integral normalized number to int64, reporting whether it
// fits. A fractional float, an out-of-range big.Int, or a non-number does not
// fit — the caller then fails the export.
func toInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int:
		return int64(t), true
	case int8:
		return int64(t), true
	case int16:
		return int64(t), true
	case int32:
		return int64(t), true
	case int64:
		return t, true
	case uint:
		return uintToInt64(uint64(t))
	case uint8:
		return int64(t), true
	case uint16:
		return int64(t), true
	case uint32:
		return int64(t), true
	case uint64:
		return uintToInt64(t)
	case float32:
		return floatToInt64(float64(t))
	case float64:
		return floatToInt64(t)
	case *big.Int:
		if t.IsInt64() {
			return t.Int64(), true
		}
		return 0, false
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n, true
		}
		if f, err := t.Float64(); err == nil {
			return floatToInt64(f)
		}
		return 0, false
	default:
		return 0, false
	}
}

// uintToInt64 converts an unsigned value to int64 when it fits.
func uintToInt64(u uint64) (int64, bool) {
	if u > math.MaxInt64 {
		return 0, false
	}
	return int64(u), true
}

// floatToInt64 converts a finite, integral float to int64 when it fits. The
// integrality check also rejects NaN (NaN != NaN), and the range check also
// rejects ±Inf (Inf is neither < MinInt64 nor < MaxInt64), so no separate
// IsNaN/IsInf guard is needed.
func floatToInt64(f float64) (int64, bool) {
	if f != math.Trunc(f) {
		return 0, false
	}
	if f < math.MinInt64 || f >= math.MaxInt64 {
		return 0, false
	}
	return int64(f), true
}

// toFloat64 converts any normalized number to float64, reporting whether v was a
// number at all.
func toFloat64(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int8:
		return float64(t), true
	case int16:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint:
		return float64(t), true
	case uint8:
		return float64(t), true
	case uint16:
		return float64(t), true
	case uint32:
		return float64(t), true
	case uint64:
		return float64(t), true
	case float32:
		return float64(t), true
	case float64:
		return t, true
	case *big.Int:
		f := new(big.Float).SetInt(t)
		out, _ := f.Float64()
		return out, true
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// canonicalJSON renders v as compact JSON with sorted object keys (encoding/json
// sorts map keys) and no HTML escaping, the byte-lossless carrier for an
// arrow.json column.
func canonicalJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("marshal json: %w", err)
	}
	return string(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// jsonTypeName names v's JSON kind for an error message, never leaking the value
// itself (which may be sensitive).
func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case float32, float64, json.Number:
		return "number"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, *big.Int:
		return "integer"
	default:
		return "value"
	}
}

package query

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strings"

	"gopkg.in/yaml.v3"
)

// dumpRecord is the on-disk envelope for one item in a typed JSONL dump. The type
// tag is what makes a Redis round-trip lossless: the normalized JSON of a hash and
// of a document are both objects, so only the tag says which to reconstruct.
type dumpRecord struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Value any    `json:"value"`
}

// WriteJSONL encodes records as a typed JSONL dump, one object per line. It is the
// inverse of a typed-mode JSONLSource, so a dump written here reloads losslessly.
func WriteJSONL(w io.Writer, recs []Record) error {
	enc := json.NewEncoder(w)
	for _, r := range recs {
		if err := enc.Encode(dumpRecord(r)); err != nil {
			return fmt.Errorf("encode dump record %q: %w", r.Key, err)
		}
	}
	return nil
}

// JSONLSource returns a RecordSource that streams a JSONL reader in pages of
// pageSize. In typed mode each line is a {key,type,value} envelope; in plain mode
// (foreign input) each line's whole JSON value becomes the record value with no
// key or type, leaving the caller's transform to key it. Numbers decode
// precisely: an integer stays exact (int or *big.Int), never a lossy float.
func JSONLSource(r io.Reader, pageSize int, plain bool) RecordSource {
	return func(ctx context.Context, fn func(batch []Record) error) error {
		sc := bufio.NewScanner(r)
		// A JSONL line can be a large document; raise the scanner's line cap well
		// above bufio's 64 KiB default. Bit-shift sizes so the constants are one
		// value each, not a product.
		const initBuf, maxLine = 64 << 10, 16 << 20
		sc.Buffer(make([]byte, 0, initBuf), maxLine)
		pg := newPager(pageSize, fn)
		line := 0
		for sc.Scan() {
			if err := ctx.Err(); err != nil {
				return err
			}
			line++
			raw := strings.TrimSpace(sc.Text())
			if raw == "" {
				continue
			}
			rec, err := decodeLine(raw, plain)
			if err != nil {
				return fmt.Errorf("line %d: %w", line, err)
			}
			if err := pg.add(rec); err != nil {
				return err
			}
		}
		if err := sc.Err(); err != nil {
			return fmt.Errorf("read jsonl: %w", err)
		}
		return pg.flush()
	}
}

// decodeLine parses one JSONL line into a Record. Typed mode expects the dump
// envelope and rejects a bare value with a hint; plain mode takes the whole value.
func decodeLine(raw string, plain bool) (Record, error) {
	if plain {
		v, err := decodeValue(raw)
		if err != nil {
			return Record{}, err
		}
		return Record{Value: v}, nil
	}
	return DecodeTypedRecord([]byte(raw))
}

// DecodeTypedRecord decodes one {key,type,value} record of a typed dump, with exact
// integers and the sign of a negative zero kept.
func DecodeTypedRecord(raw []byte) (Record, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var env dumpRecord
	if err := dec.Decode(&env); err != nil {
		return Record{}, fmt.Errorf("expected a {key,type,value} record (use --key-field for foreign JSON): %w", err)
	}
	if env.Key == "" {
		return Record{}, fmt.Errorf("record has no key (use --key-field for foreign JSON)")
	}
	return Record{Key: env.Key, Type: env.Type, Value: convertNumbers(env.Value)}, nil
}

// decodeValue parses a single JSON value with exact integers preserved.
func decodeValue(raw string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	return convertNumbers(v), nil
}

// convertNumbers walks a decoded value, turning each json.Number into an exact Go
// number: an integer becomes an int or *big.Int, a fractional number a float64.
// This mirrors the read path's number handling so a value keeps its precision
// across a dump/reload, rather than every integer collapsing to a float.
func convertNumbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		return convertNumber(t)
	case map[string]any:
		for k, e := range t {
			t[k] = convertNumbers(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = convertNumbers(e)
		}
		return t
	default:
		return t
	}
}

// convertNumber turns one json.Number into an exact Go number: an integer becomes
// an int or *big.Int, a fractional number a float64, and a number that float64
// cannot hold stays its literal text.
func convertNumber(t json.Number) any {
	s := t.String()
	// -0 is the one integer literal an int cannot hold: keep it as the float64
	// negative zero, so a dump of -0.0 reads back with its sign (numfmt does
	// the same for the backend adapters).
	if s == "-0" {
		return math.Copysign(0, -1)
	}
	if !strings.ContainsAny(s, ".eE") {
		if i, err := t.Int64(); err == nil && int64(int(i)) == i {
			return int(i)
		}
		if bi, ok := new(big.Int).SetString(s, 10); ok {
			return bi
		}
	}
	if f, err := t.Float64(); err == nil {
		return f
	}
	return s
}

// JSONSource streams typed {key,type,value} records (or, in plain mode, whole
// values) from a JSON reader that is either concatenated objects (JSON Lines) or a
// single top-level array — the array-tolerant counterpart of JSONLSource, so a
// --typed dump written as --jsonl or --jsona both re-import. Integers stay
// exact.
func JSONSource(r io.Reader, pageSize int, plain bool) RecordSource {
	return func(ctx context.Context, fn func(batch []Record) error) error {
		stream, err := NewJSONStream(r)
		if err != nil {
			return err
		}
		if stream == nil {
			return nil // empty input: no records, and no context check.
		}
		pg := newPager(pageSize, fn)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			raw, ok, err := stream.Next()
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			rec, err := decodeLine(string(raw), plain)
			if err != nil {
				return err
			}
			if err := pg.add(rec); err != nil {
				return err
			}
		}
		return pg.flush()
	}
}

// JSONStream reads JSON records from concatenated values or from one top-level
// array, the two layouts a JSON dump can have.
type JSONStream struct {
	dec   *json.Decoder
	array bool
}

// NewJSONStream reads the layout prologue of r. Empty or whitespace-only input
// gives a nil stream and no error.
func NewJSONStream(r io.Reader) (*JSONStream, error) {
	br := bufio.NewReader(r)
	array, err := startsJSONArray(br)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, fmt.Errorf("read json: %w", err)
	}
	dec := json.NewDecoder(br)
	if array {
		if _, err := dec.Token(); err != nil { // consume '['
			return nil, fmt.Errorf("read json array: %w", err)
		}
	}
	return &JSONStream{dec: dec, array: array}, nil
}

// Next returns the raw bytes of the next record, or ok=false at the end. An array
// ends at its closing bracket, and a stream of values ends at end of input.
func (s *JSONStream) Next() (raw json.RawMessage, ok bool, err error) {
	if s.array && !s.dec.More() {
		return nil, false, nil
	}
	if err := s.dec.Decode(&raw); err != nil {
		if !s.array && errors.Is(err, io.EOF) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("decode json record: %w", err)
	}
	return raw, true, nil
}

// startsJSONArray reports whether the first non-whitespace byte is '[' (a top-level
// array), without consuming any value bytes.
func startsJSONArray(br *bufio.Reader) (bool, error) {
	for {
		b, err := br.Peek(1)
		if err != nil {
			return false, err
		}
		switch b[0] {
		case ' ', '\t', '\r', '\n':
			if _, err := br.Discard(1); err != nil {
				return false, err
			}
		case '[':
			return true, nil
		default:
			return false, nil
		}
	}
}

// YAMLSource streams typed {key,type,value} records (or, in plain mode, whole
// values) from a multi-document YAML reader, so a --typed --yaml dump re-imports.
func YAMLSource(r io.Reader, pageSize int, plain bool) RecordSource {
	return func(ctx context.Context, fn func(batch []Record) error) error {
		dec := yaml.NewDecoder(r)
		pg := newPager(pageSize, fn)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			var v any
			err := dec.Decode(&v)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("decode yaml record: %w", err)
			}
			rec, err := recordFromDecoded(v, plain)
			if err != nil {
				return err
			}
			if err := pg.add(rec); err != nil {
				return err
			}
		}
		return pg.flush()
	}
}

// pager hands records to fn in pages of size, reusing one slice. A caller of fn
// must not keep the page.
type pager struct {
	page []Record
	size int
	fn   func([]Record) error
}

// newPager returns a pager for fn. A size of 0 or less selects 100.
func newPager(size int, fn func([]Record) error) *pager {
	if size <= 0 {
		size = 100
	}
	return &pager{page: make([]Record, 0, size), size: size, fn: fn}
}

// add appends r and hands a full page to fn.
func (p *pager) add(r Record) error {
	p.page = append(p.page, r)
	if len(p.page) < p.size {
		return nil
	}
	if err := p.fn(p.page); err != nil {
		return err
	}
	p.page = p.page[:0]
	return nil
}

// flush hands a non-empty tail to fn.
func (p *pager) flush() error {
	if len(p.page) == 0 {
		return nil
	}
	return p.fn(p.page)
}

// recordFromDecoded turns an already-decoded value (a YAML document) into a Record:
// in plain mode the whole value; in typed mode a {key,type,value} map envelope.
func recordFromDecoded(v any, plain bool) (Record, error) {
	if plain {
		return Record{Value: convertNumbers(v)}, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return Record{}, fmt.Errorf("expected a {key,type,value} record, got %T", v)
	}
	key, _ := obj["key"].(string)
	if key == "" {
		return Record{}, fmt.Errorf("record has no key")
	}
	typ, _ := obj["type"].(string)
	return Record{Key: key, Type: typ, Value: convertNumbers(obj["value"])}, nil
}

package query

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if pageSize <= 0 {
		pageSize = 100
	}
	return func(ctx context.Context, fn func(batch []Record) error) error {
		sc := bufio.NewScanner(r)
		// A JSONL line can be a large document; raise the scanner's line cap well
		// above bufio's 64 KiB default. Bit-shift sizes so the constants are one
		// value each, not a product.
		const initBuf, maxLine = 64 << 10, 16 << 20
		sc.Buffer(make([]byte, 0, initBuf), maxLine)
		page := make([]Record, 0, pageSize)
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
			page = append(page, rec)
			if len(page) >= pageSize {
				if err := fn(page); err != nil {
					return err
				}
				page = page[:0]
			}
		}
		if err := sc.Err(); err != nil {
			return fmt.Errorf("read jsonl: %w", err)
		}
		if len(page) > 0 {
			return fn(page)
		}
		return nil
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
	dec := json.NewDecoder(strings.NewReader(raw))
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
		s := t.String()
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

// JSONSource streams typed {key,type,value} records (or, in plain mode, whole
// values) from a JSON reader that is either concatenated objects (JSON Lines) or a
// single top-level array — the array-tolerant counterpart of JSONLSource, so a
// --typed dump written as --jsonl or --json-array both re-import. Integers stay
// exact.
func JSONSource(r io.Reader, pageSize int, plain bool) RecordSource {
	if pageSize <= 0 {
		pageSize = 100
	}
	return func(ctx context.Context, fn func(batch []Record) error) error {
		br := bufio.NewReader(r)
		array, err := startsJSONArray(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read json: %w", err)
		}
		dec := json.NewDecoder(br)
		if array {
			if _, err := dec.Token(); err != nil { // consume '['
				return fmt.Errorf("read json array: %w", err)
			}
		}
		page := make([]Record, 0, pageSize)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			if array && !dec.More() {
				break
			}
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				if !array && errors.Is(err, io.EOF) {
					break
				}
				return fmt.Errorf("decode json record: %w", err)
			}
			rec, err := decodeLine(string(raw), plain)
			if err != nil {
				return err
			}
			page = append(page, rec)
			if len(page) >= pageSize {
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
	}
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
	if pageSize <= 0 {
		pageSize = 100
	}
	return func(ctx context.Context, fn func(batch []Record) error) error {
		dec := yaml.NewDecoder(r)
		page := make([]Record, 0, pageSize)
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
			page = append(page, rec)
			if len(page) >= pageSize {
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
	}
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

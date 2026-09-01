package file

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"go.mongodb.org/mongo-driver/v2/bson"

	iqmongo "github.com/zsltg/iq/drivers/mongo"
	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// bsonSource streams a mongodump .bson file — concatenated raw BSON documents,
// each framed by a leading little-endian int32 length. Each document is decoded
// and normalized to the exact shape a live collection scan produces (see
// drivers/mongo), keyed by its _id.
func bsonSource(r io.Reader, size int, dec numfmt.DecimalMode) query.RecordSource {
	return func(ctx context.Context, fn func(batch []query.Record) error) error {
		br := bufio.NewReader(r)
		page := make([]query.Record, 0, size)
		var lenBuf [4]byte
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := io.ReadFull(br, lenBuf[:])
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("read bson length: %w", err)
			}
			// The frame comes from an untrusted dump, and the buffer below is sized
			// from it. A length outside BSON's own limits is rejected before the
			// allocation, so a corrupt or hostile header cannot ask for 4 GiB.
			docLen := int(binary.LittleEndian.Uint32(lenBuf[:]))
			if docLen < bsonMinDoc || docLen > bsonMaxDoc {
				return fmt.Errorf("invalid bson document length %d", docLen)
			}
			doc := make([]byte, docLen)
			copy(doc, lenBuf[:])
			if _, err := io.ReadFull(br, doc[4:]); err != nil {
				return fmt.Errorf("read bson document: %w", err)
			}
			rec, err := recordForBSON(doc, dec)
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
	}
}

// extJSONSource streams mongoexport output: Extended JSON documents, one per line
// (the default) or a single JSON array (--jsonArray). Each document is normalized
// like a live scan and keyed by _id.
func extJSONSource(r io.Reader, size int, dec numfmt.DecimalMode) query.RecordSource {
	return func(ctx context.Context, fn func(batch []query.Record) error) error {
		br := bufio.NewReader(r)
		array, err := startsArray(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read mongoexport json: %w", err)
		}
		jd := json.NewDecoder(br)
		page := make([]query.Record, 0, size)
		flush := func() error {
			if len(page) == 0 {
				return nil
			}
			if err := fn(page); err != nil {
				return err
			}
			page = page[:0]
			return nil
		}
		if array {
			if _, err := jd.Token(); err != nil { // consume '['
				return fmt.Errorf("read mongoexport array: %w", err)
			}
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			if array && !jd.More() {
				break
			}
			var raw json.RawMessage
			if err := jd.Decode(&raw); err != nil {
				if !array && errors.Is(err, io.EOF) {
					break
				}
				return fmt.Errorf("decode mongoexport json: %w", err)
			}
			rec, err := recordForExtJSON(raw, dec)
			if err != nil {
				return err
			}
			page = append(page, rec)
			if len(page) >= size {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		return flush()
	}
}

// startsArray reports whether the first non-whitespace byte is a '[' (a
// --jsonArray dump), without consuming any document bytes.
func startsArray(br *bufio.Reader) (bool, error) {
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

// recordForBSON decodes one raw BSON document into a typed record.
func recordForBSON(raw []byte, dec numfmt.DecimalMode) (query.Record, error) {
	var doc bson.M
	if err := bson.Unmarshal(raw, &doc); err != nil {
		return query.Record{}, fmt.Errorf("decode bson document: %w", err)
	}
	return recordForDoc(doc, dec), nil
}

// recordForExtJSON decodes one Extended JSON document into a typed record.
func recordForExtJSON(raw json.RawMessage, dec numfmt.DecimalMode) (query.Record, error) {
	var doc bson.M
	if err := bson.UnmarshalExtJSON(raw, true, &doc); err != nil {
		return query.Record{}, fmt.Errorf("decode extended json document: %w", err)
	}
	return recordForDoc(doc, dec), nil
}

// recordForDoc turns a decoded document into the record a live Mongo scan yields:
// keyed by _id, type "document", value the normalized document.
func recordForDoc(doc bson.M, dec numfmt.DecimalMode) query.Record {
	key := ""
	if id, ok := doc["_id"]; ok {
		key = iqmongo.KeyOf(id)
	}
	val := iqmongo.Normalize(doc, dec)
	return query.Record{Key: key, Type: "document", Value: val}
}

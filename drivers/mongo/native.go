package mongo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/zsltg/iq/internal/query"
)

// The native form of this driver is query.FormMongoEJSON: one document in
// canonical Extended JSON, where every BSON type has a tagged form, for example
// {"n": {"$numberInt": "7"}} or {"_id": {"$oid": "..."}}. Unlike the normalized
// form, it keeps int32 apart from int64, dates, Decimal128, binary data, and
// every other BSON type. NativeScan reads it and Put writes it. NativeRecord is
// the shared encoder of the offline dump reader.

// NativeForm names the form that NativeScan emits.
func (s *Store) NativeForm() query.Form { return query.FormMongoEJSON }

// WritesForm names the native form that Put accepts besides normalized JSON.
func (s *Store) WritesForm() query.Form { return query.FormMongoEJSON }

// NativeScan streams the whole collection as records in the native form. The key
// of a record is the same key that TypedScan gives. The value is the canonical
// Extended JSON of the document, as plain Go maps, slices, strings, booleans, and
// json.Number, so a jq filter runs on it. The scan holds one page at a time.
func (s *Store) NativeScan(ctx context.Context, fn func(batch []query.Record) error) error {
	if s.collection == "" {
		return errNoCollection
	}
	opts := options.Find().SetBatchSize(int32(s.pageSize)) //nolint:gosec // G115: pageSize is the small compile-time scanBatch constant.
	cur, err := s.db.Collection(s.collection).Find(ctx, bson.M{}, opts)
	if err != nil {
		return fmt.Errorf("mongodb find: %w", selectionCause(err))
	}
	defer func() { _ = cur.Close(ctx) }()

	page := make([]query.Record, 0, s.pageSize)
	for cur.Next(ctx) {
		// The cursor reuses the buffer of Current, so the encoder copies what it keeps.
		rec, err := NativeRecord(cur.Current)
		if err != nil {
			return err
		}
		page = append(page, rec)
		if len(page) >= s.pageSize {
			if err := fn(page); err != nil {
				return err
			}
			page = make([]query.Record, 0, s.pageSize)
		}
	}
	if err := cur.Err(); err != nil {
		return fmt.Errorf("mongodb cursor: %w", selectionCause(err))
	}
	if len(page) > 0 {
		return fn(page)
	}
	return nil
}

// NativeRecord turns one raw BSON document into a record in the native form. The
// key is the _id as KeyOf renders it. NativeRecord is exported so a dump reader
// builds the same record as a live scan.
func NativeRecord(doc bson.Raw) (query.Record, error) {
	var id any
	if rv := doc.Lookup("_id"); rv.Type != 0 {
		if err := rv.Unmarshal(&id); err != nil {
			return query.Record{}, fmt.Errorf("mongodb decode: _id: %w", err)
		}
	}
	key := KeyOf(id)
	text, err := bson.MarshalExtJSON(doc, true, false)
	if err != nil {
		return query.Record{}, fmt.Errorf("mongodb: document %q: encode extended json: %w", key, err)
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	var val map[string]any
	if err := dec.Decode(&val); err != nil {
		return query.Record{}, fmt.Errorf("mongodb: document %q: read extended json: %w", key, err)
	}
	return query.Record{Key: key, Type: "document", Form: query.FormMongoEJSON, Value: val}, nil
}

// nativeDocument builds the BSON document of a record in the native form. It
// decodes the value strictly as Extended JSON, so a malformed tag is an error
// that names the key. The _id keeps its type when its display form equals the
// record key. Otherwise the record key wins, as for a normalized record. A
// keyless record keeps the _id of its value, or gets a minted one.
func nativeDocument(r query.Record) (bson.D, error) {
	obj, ok := r.Value.(map[string]any)
	if !ok {
		return nil, query.NonObjectValueError("mongodb", r.Key)
	}
	text, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("mongodb: key %q: read native value: %w", r.Key, err)
	}
	var doc bson.D
	if err := bson.UnmarshalExtJSON(text, true, &doc); err != nil {
		return nil, fmt.Errorf("mongodb: key %q: value is not valid canonical Extended JSON: %w", r.Key, err)
	}
	return withID(r.Key, doc), nil
}

// withID returns doc with its _id first. A record key that differs from the
// display form of the typed _id replaces that _id with idValue(key). A keyless
// record keeps the _id of doc, if any.
func withID(key string, doc bson.D) bson.D {
	out := make(bson.D, 0, len(doc)+1)
	var typed any
	var hasID bool
	rest := make(bson.D, 0, len(doc))
	for _, e := range doc {
		if e.Key == "_id" {
			typed, hasID = e.Value, true
			continue
		}
		rest = append(rest, e)
	}
	switch {
	case key == "" && hasID:
		out = append(out, bson.E{Key: "_id", Value: typed})
	case key != "" && hasID && KeyOf(typed) == key:
		out = append(out, bson.E{Key: "_id", Value: typed})
	case key != "":
		out = append(out, bson.E{Key: "_id", Value: idValue(key)})
	}
	return append(out, rest...)
}

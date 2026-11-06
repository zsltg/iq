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

	return pageNative(ctx, s.pageSize, cur.Next, func() bson.Raw { return cur.Current }, cur.Err, fn)
}

// pageNative reads documents with next and current, and hands fn the native records
// in pages of size records. cursorErr reports the error that ended the read. It
// takes the parts of a cursor as functions, so a test can run it without a server.
func pageNative(ctx context.Context, size int, next func(context.Context) bool, current func() bson.Raw, cursorErr func() error, fn func(batch []query.Record) error) error {
	pager := &nativePager{size: size, fn: fn}
	for next(ctx) {
		// The cursor reuses the buffer of Current, so the encoder copies what it keeps.
		rec, err := NativeRecord(current())
		if err != nil {
			return err
		}
		if err := pager.add(rec); err != nil {
			return err
		}
	}
	if err := cursorErr(); err != nil {
		return fmt.Errorf("mongodb cursor: %w", selectionCause(err))
	}
	return pager.flush()
}

// nativePager collects records and hands them to fn in pages of size records. It
// starts a new slice for each page, so fn can keep the page that it gets.
type nativePager struct {
	size int
	fn   func(batch []query.Record) error
	page []query.Record
}

// add appends rec and hands the page to fn when the page is full.
func (p *nativePager) add(rec query.Record) error {
	p.page = append(p.page, rec)
	if len(p.page) < p.size {
		return nil
	}
	page := p.page
	p.page = nil
	return p.fn(page)
}

// flush hands the last, partial page to fn.
func (p *nativePager) flush() error {
	if len(p.page) == 0 {
		return nil
	}
	return p.fn(p.page)
}

// NativeRecord turns one raw BSON document into a record in the native form. The
// key is the _id as KeyOf renders it, or empty when the document has no _id. NativeRecord is exported so a dump reader
// builds the same record as a live scan.
func NativeRecord(doc bson.Raw) (query.Record, error) {
	key := ""
	if rv := doc.Lookup("_id"); rv.Type != 0 {
		var id any
		if err := rv.Unmarshal(&id); err != nil {
			return query.Record{}, fmt.Errorf("mongodb decode: _id: %w", err)
		}
		key = KeyOf(id)
	}
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

// nativeDocument builds the BSON document of a record in the native form, and
// returns the _id that the document holds (nil when it has none). It decodes the
// value strictly as Extended JSON, so a malformed tag is an error that names the
// key. The _id keeps its type when its display form equals the record key.
// Otherwise the record key wins, as for a normalized record. A keyless record
// keeps the _id of its value, if any.
func nativeDocument(r query.Record) (bson.D, any, error) {
	obj, ok := r.Value.(map[string]any)
	if !ok {
		return nil, nil, query.NonObjectValueError("mongodb", r.Key)
	}
	text, err := json.Marshal(obj)
	if err != nil {
		return nil, nil, fmt.Errorf("mongodb: key %q: read native value: %w", r.Key, err)
	}
	var doc bson.D
	if err := bson.UnmarshalExtJSON(text, true, &doc); err != nil {
		return nil, nil, fmt.Errorf("mongodb: key %q: value is not valid canonical Extended JSON: %w", r.Key, err)
	}
	out, id := withID(r.Key, doc)
	return out, id, nil
}

// withID returns doc with its _id first, and that _id. A record key that differs
// from the display form of the typed _id replaces that _id with idValue(key). A
// keyless record keeps the _id of doc, if any. The id is nil when the result has
// no _id.
func withID(key string, doc bson.D) (bson.D, any) {
	var typed any
	var hasID bool
	rest := make(bson.D, 0, len(doc)+1)
	for _, e := range doc {
		if e.Key == "_id" {
			typed, hasID = e.Value, true
			continue
		}
		rest = append(rest, e)
	}
	var id any
	switch {
	case hasID && (key == "" || KeyOf(typed) == key):
		id = typed
	case key != "":
		id = idValue(key)
	default:
		return rest, nil
	}
	return append(bson.D{{Key: "_id", Value: id}}, rest...), id
}

package mongo

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/zsltg/iq/internal/query"
)

// duplicateKeyCode is MongoDB's error code for a unique-index (including _id)
// collision, used to count InsertOnly skips without treating them as failures.
const duplicateKeyCode = 11000

// Put writes a batch of records into the collection, keyed by _id. Upsert
// replaces an existing document and inserts a new one. InsertOnly inserts new
// documents and skips (counts) those whose _id already exists. Every value is
// marshalled through bson and is never built as a string. A value that is not a
// JSON object is a returned error. So a scalar is never silently wrapped and a
// copy round-trips exactly.
func (s *Store) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	if err := query.CheckForms("mongo", query.FormJSON, batch); err != nil {
		return query.WriteStat{}, err
	}
	if s.collection == "" {
		return query.WriteStat{}, errNoCollection
	}
	if len(batch) == 0 {
		return query.WriteStat{}, nil
	}
	coll := s.db.Collection(s.collection)
	if mode == query.InsertOnly {
		return insertOnly(ctx, coll, batch)
	}
	return upsert(ctx, coll, batch)
}

// upsert replaces each record's document by _id, creating it when absent. The bulk
// result's matched count is the number overwritten, the upserted count the number
// newly inserted.
func upsert(ctx context.Context, coll *mongo.Collection, batch []query.Record) (query.WriteStat, error) {
	models := make([]mongo.WriteModel, len(batch))
	for i, r := range batch {
		doc, err := documentFor(r)
		if err != nil {
			return query.WriteStat{}, err
		}
		if r.Key == "" {
			// A keyless record has no identity to replace by, so it is always a fresh
			// insert with a minted _id (foreign JSON into MongoDB).
			models[i] = mongo.NewInsertOneModel().SetDocument(doc)
			continue
		}
		models[i] = mongo.NewReplaceOneModel().
			SetFilter(bson.M{"_id": idValue(r.Key)}).
			SetReplacement(doc).
			SetUpsert(true)
	}
	res, err := coll.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	if err != nil {
		return query.WriteStat{}, fmt.Errorf("mongodb bulk write: %w", selectionCause(err))
	}
	return query.WriteStat{
		Written:     int(res.UpsertedCount + res.InsertedCount),
		Overwritten: int(res.MatchedCount),
	}, nil
}

// insertOnly inserts new documents and counts duplicate-key collisions as skips.
// The unordered insert continues past a collision. Any non-duplicate write error
// fails the batch.
func insertOnly(ctx context.Context, coll *mongo.Collection, batch []query.Record) (query.WriteStat, error) {
	docs := make([]any, len(batch))
	for i, r := range batch {
		doc, err := documentFor(r)
		if err != nil {
			return query.WriteStat{}, err
		}
		docs[i] = doc
	}
	res, err := coll.InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	if err == nil {
		return query.WriteStat{Written: len(res.InsertedIDs)}, nil
	}
	var bwe mongo.BulkWriteException
	if !errors.As(err, &bwe) {
		return query.WriteStat{}, fmt.Errorf("mongodb insert: %w", selectionCause(err))
	}
	skipped := 0
	for _, we := range bwe.WriteErrors {
		if we.Code != duplicateKeyCode {
			return query.WriteStat{}, fmt.Errorf("mongodb insert: %w", err)
		}
		skipped++
	}
	return query.WriteStat{Written: len(batch) - skipped, Skipped: skipped}, nil
}

// documentFor builds the BSON document to write for a record, with _id set from
// the key. So the record key, not the value, supplies the identity. A 24-hex
// key restores an ObjectID _id. A keyless record (foreign JSON with no key
// field) lets MongoDB mint the _id. The value must be a JSON object. A scalar
// is rejected and not wrapped as {value: ...}, so a successful Put round-trips
// exactly.
func documentFor(r query.Record) (bson.M, error) {
	obj, ok := r.Value.(map[string]any)
	if !ok {
		return nil, query.NonObjectValueError("mongodb", r.Key)
	}
	// Identity comes from the record key, replacing any read _id.
	rest := maps.Clone(obj)
	delete(rest, "_id")
	doc, err := bsonWalk{key: r.Key}.object("", rest)
	if err != nil {
		return nil, err
	}
	if r.Key != "" {
		doc["_id"] = idValue(r.Key)
	}
	return doc, nil
}

// maxDecimal128Digits is the number of significant digits that a Decimal128
// holds exactly. A longer coefficient would round.
const maxDecimal128Digits = 34

// bsonWalk converts the values of one record. It carries the record key, which
// every error names.
type bsonWalk struct{ key string }

// bsonPath names a value inside a record value, like "a.b[2]". The top level has
// an empty path.
type bsonPath string

// field is the path of the entry name in the object at p.
func (p bsonPath) field(name string) bsonPath {
	if p == "" {
		return bsonPath(name)
	}
	return p + "." + bsonPath(name)
}

// element is the path of the entry i in the array at p.
func (p bsonPath) element(i int) bsonPath {
	return p + "[" + bsonPath(strconv.Itoa(i)) + "]"
}

// label names the path in an error.
func (p bsonPath) label() string {
	if p == "" {
		return "the value"
	}
	return "field " + strconv.Quote(string(p))
}

// value converts the JSON-ready value at path into a BSON-encodable one. It
// recurses into objects and arrays. BSON cannot encode a big integer. So a big
// integer becomes an int64 if it fits, else a Decimal128 with the exact value.
// An integer that a Decimal128 cannot hold is an error.
func (w bsonWalk) value(path bsonPath, v any) (any, error) {
	switch t := v.(type) {
	case *big.Int:
		return w.integer(path, t)
	case map[string]any:
		return w.object(path, t)
	case []any:
		return w.array(path, t)
	default:
		return t, nil
	}
}

// object converts the entries of the object at path. It goes through the names in
// sorted order, so the same input always gives the same error.
func (w bsonWalk) object(path bsonPath, obj map[string]any) (bson.M, error) {
	out := make(bson.M, len(obj))
	for _, name := range slices.Sorted(maps.Keys(obj)) {
		e, err := w.value(path.field(name), obj[name])
		if err != nil {
			return nil, err
		}
		out[name] = e
	}
	return out, nil
}

// array converts the elements of the array at path in order.
func (w bsonWalk) array(path bsonPath, list []any) (bson.A, error) {
	out := make(bson.A, len(list))
	for i, e := range list {
		c, err := w.value(path.element(i), e)
		if err != nil {
			return nil, err
		}
		out[i] = c
	}
	return out, nil
}

// integer converts the big integer n at path. An int64 stays an int64. Any other
// integer becomes a Decimal128 with the same value, or an error when it has more
// than 34 significant digits or is larger than a Decimal128 holds.
func (w bsonWalk) integer(path bsonPath, n *big.Int) (any, error) {
	if n.IsInt64() {
		return n.Int64(), nil
	}
	text := n.String()
	digits := strings.TrimLeft(text, "-")
	coefficient := strings.TrimRight(digits, "0")
	zeros := len(digits) - len(coefficient)
	if len(coefficient) > maxDecimal128Digits {
		return nil, fmt.Errorf("mongodb: key %q: %s: integer %s (%d digits) has more digits than a Decimal128 holds (%d)",
			w.key, path.label(), shortDigits(digits), len(digits), maxDecimal128Digits)
	}
	if len(digits) > maxDecimal128Digits {
		// Keep the trailing zeros as an exponent: the coefficient is too short for them.
		text = text[:len(text)-zeros] + "E+" + strconv.Itoa(zeros)
	}
	// The parse accepts every value that a Decimal128 holds, up to
	// 9.999999999999999999999999999999999E+6144, and fails above that. Its error
	// quotes the whole input, which can hold thousands of digits, so it is not kept.
	d, err := bson.ParseDecimal128(text)
	if err != nil {
		return nil, fmt.Errorf("mongodb: key %q: %s: integer %s (%d digits) is too large for a Decimal128",
			w.key, path.label(), shortDigits(digits), len(digits))
	}
	return d, nil
}

// shortDigits shortens a digit string for an error: the first 10 digits, then
// "...". An integer in an error always has more than 10 digits.
func shortDigits(digits string) string {
	return digits[:10] + "..."
}

// idValue turns a string key into the _id to match or insert. It returns the
// ObjectID that the key encodes when the key is 24-character hex (restoring the
// identity of an ObjectID-keyed collection). Otherwise it returns the string
// itself. It is the write-side inverse of keyOf.
func idValue(key string) any {
	if oid, err := bson.ObjectIDFromHex(key); err == nil {
		return oid
	}
	return key
}

// Clear empties the collection, keeping it and its indexes (the `iq data clear`
// semantics for a document store).
func (s *Store) Clear(ctx context.Context) error {
	if s.collection == "" {
		return errNoCollection
	}
	if _, err := s.db.Collection(s.collection).DeleteMany(ctx, bson.M{}); err != nil {
		return fmt.Errorf("mongodb delete: %w", selectionCause(err))
	}
	return nil
}

// Drop removes the collection entirely: documents and indexes (the `iq data
// drop` semantics). The Store implements Dropper because a MongoDB collection is
// a removable container, unlike a Redis DB index.
func (s *Store) Drop(ctx context.Context) error {
	if s.collection == "" {
		return errNoCollection
	}
	if err := s.db.Collection(s.collection).Drop(ctx); err != nil {
		return fmt.Errorf("mongodb drop: %w", selectionCause(err))
	}
	return nil
}

// Delete removes the named keys with deleteMany({_id: {$in: ids}}), chunked to
// pageSize so a large list is bounded per round-trip. Each key is mapped through
// idValue (24-hex restores an ObjectID). DeletedCount is exact, so Deleted is the
// removed count and Missing is the remainder. The caller dedupes the keys, so the
// count maps one-to-one. The ids ride as a bound bson value and are never
// concatenated.
func (s *Store) Delete(ctx context.Context, keys []string) (query.DeleteStat, error) {
	if s.collection == "" {
		return query.DeleteStat{}, errNoCollection
	}
	coll := s.db.Collection(s.collection)
	var stat query.DeleteStat
	for chunk := range slices.Chunk(keys, s.pageSize) {
		ids := make(bson.A, len(chunk))
		for i, k := range chunk {
			ids[i] = idValue(k)
		}
		res, err := coll.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": ids}})
		if err != nil {
			return query.DeleteStat{}, fmt.Errorf("mongodb delete: %w", selectionCause(err))
		}
		stat.Deleted += int(res.DeletedCount)
		stat.Missing += len(chunk) - int(res.DeletedCount)
	}
	return stat, nil
}

// TypedScan streams the whole collection as typed records, reusing the
// ScanBatches cursor. Every item is a document, so the type tag is "document".
// The key is the _id string and the value is the normalized document.
func (s *Store) TypedScan(ctx context.Context, fn func(batch []query.Record) error) error {
	return s.ScanBatches(ctx, func(batch map[string]any) error {
		recs := make([]query.Record, 0, len(batch))
		for k, v := range batch {
			recs = append(recs, query.Record{Key: k, Type: "document", Value: v})
		}
		return fn(recs)
	})
}

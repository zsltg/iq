package mongo

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/zsltg/iq/internal/query"
)

// duplicateKeyCode is MongoDB's error code for a unique-index (including _id)
// collision, used to count InsertOnly skips without treating them as failures.
const duplicateKeyCode = 11000

// Put writes a batch of records into the collection, keyed by _id. Upsert replaces
// an existing document and inserts a new one; InsertOnly inserts new documents and
// skips (counts) those whose _id already exists. Every value is marshalled through
// bson — never string-built — and a value that is not a JSON object is a returned
// error, so a scalar is never silently wrapped and a copy round-trips exactly.
func (s *Store) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
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
		return query.WriteStat{}, fmt.Errorf("mongodb bulk write: %w", err)
	}
	return query.WriteStat{
		Written:     int(res.UpsertedCount + res.InsertedCount),
		Overwritten: int(res.MatchedCount),
	}, nil
}

// insertOnly inserts new documents and counts duplicate-key collisions as skips.
// The unordered insert continues past a collision; any non-duplicate write error
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
		return query.WriteStat{}, fmt.Errorf("mongodb insert: %w", err)
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

// documentFor builds the BSON document to write for a record, with _id set from the
// key so identity is carried by the key, not the value. A 24-hex key restores an
// ObjectID _id; a keyless record (foreign JSON with no key field) lets MongoDB mint the
// _id. The value must be a JSON object: a scalar is rejected rather than wrapped as
// {value: ...}, so a successful Put round-trips exactly.
func documentFor(r query.Record) (bson.M, error) {
	obj, ok := r.Value.(map[string]any)
	if !ok {
		return nil, query.NonObjectValueError("mongodb", r.Key)
	}
	doc := bson.M{}
	for k, v := range obj {
		if k == "_id" {
			continue // identity comes from the record key, replacing any read _id.
		}
		doc[k] = bsonValue(v)
	}
	if r.Key != "" {
		doc["_id"] = idValue(r.Key)
	}
	return doc, nil
}

// bsonValue converts a JSON-ready value into a BSON-encodable one, recursing into
// objects and arrays and rendering a big integer (which BSON cannot encode) as an
// int64 when it fits, else its decimal string, so a cross-driver copy never fails
// to marshal.
func bsonValue(v any) any {
	switch t := v.(type) {
	case *big.Int:
		if t.IsInt64() {
			return t.Int64()
		}
		return t.String()
	case map[string]any:
		out := make(bson.M, len(t))
		for k, e := range t {
			out[k] = bsonValue(e)
		}
		return out
	case []any:
		out := make(bson.A, len(t))
		for i, e := range t {
			out[i] = bsonValue(e)
		}
		return out
	default:
		return t
	}
}

// idValue turns a string key into the _id to match or insert: the ObjectID it
// encodes when the key is 24-character hex (restoring an ObjectID-keyed
// collection's identity), else the string itself. It is the write-side inverse of
// keyOf.
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
		return fmt.Errorf("mongodb delete: %w", err)
	}
	return nil
}

// Drop removes the collection entirely — documents and indexes (the `iq data drop`
// semantics). The Store implements Dropper because a MongoDB collection is a
// removable container, unlike a Redis DB index.
func (s *Store) Drop(ctx context.Context) error {
	if s.collection == "" {
		return errNoCollection
	}
	if err := s.db.Collection(s.collection).Drop(ctx); err != nil {
		return fmt.Errorf("mongodb drop: %w", err)
	}
	return nil
}

// Delete removes the named keys with deleteMany({_id: {$in: ids}}), chunked to
// pageSize so a large list is bounded per round-trip. Each key is mapped through
// idValue (24-hex restores an ObjectID). DeletedCount is exact, so Deleted is the
// removed count and Missing the remainder; keys are deduped by the caller, so the
// count maps one-to-one. The ids ride as a bound bson value, never concatenated.
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
			return query.DeleteStat{}, fmt.Errorf("mongodb delete: %w", err)
		}
		stat.Deleted += int(res.DeletedCount)
		stat.Missing += len(chunk) - int(res.DeletedCount)
	}
	return stat, nil
}

// TypedScan streams the whole collection as typed records, reusing the ScanBatches
// cursor. Every item is a document, so the type tag is "document"; the key is the
// _id string and the value is the normalized document.
func (s *Store) TypedScan(ctx context.Context, fn func(batch []query.Record) error) error {
	return s.ScanBatches(ctx, func(batch map[string]any) error {
		recs := make([]query.Record, 0, len(batch))
		for k, v := range batch {
			recs = append(recs, query.Record{Key: k, Type: "document", Value: v})
		}
		return fn(recs)
	})
}

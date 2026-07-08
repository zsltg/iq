package couchdb

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/go-kivik/kivik/v4"

	"github.com/zsltg/iq/internal/query"
)

// Put writes a batch of records into the database, keyed by _id. Upsert replaces an
// existing document (fetching its current _rev first, as CouchDB requires) and
// inserts a new one; InsertOnly inserts new documents and skips (counts) those whose
// _id already exists, reported by CouchDB as a per-document conflict. A keyless
// record (foreign JSON with no key) lets CouchDB mint the _id. Every value is
// marshalled as JSON by the driver — never string-built.
func (s *Store) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	if s.db == "" {
		return query.WriteStat{}, errNoDatabase
	}
	if len(batch) == 0 {
		return query.WriteStat{}, nil
	}
	if mode == query.InsertOnly {
		return s.insertOnly(ctx, batch)
	}
	return s.upsert(ctx, batch)
}

// upsert replaces each keyed record's document by _id, creating it when absent. It
// first reads the current _rev of the keys that already exist (CouchDB rejects an
// update without the matching _rev), attaches them, and bulk-writes. A record whose
// key had a prior revision counts as overwritten, the rest as written.
func (s *Store) upsert(ctx context.Context, batch []query.Record) (query.WriteStat, error) {
	db := s.client.DB(s.db)
	revs, err := s.currentRevs(ctx, batch)
	if err != nil {
		return query.WriteStat{}, err
	}

	docs := make([]any, len(batch))
	for i, r := range batch {
		doc, err := documentBody(r)
		if err != nil {
			return query.WriteStat{}, err
		}
		if r.Key != "" {
			doc["_id"] = r.Key
			if rev, ok := revs[r.Key]; ok {
				doc["_rev"] = rev
			}
		}
		docs[i] = doc
	}

	results, err := db.BulkDocs(ctx, docs)
	if err != nil {
		return query.WriteStat{}, fmt.Errorf("couchdb bulk write: %w", err)
	}
	var stat query.WriteStat
	for i, res := range results {
		if res.Error != nil {
			return query.WriteStat{}, fmt.Errorf("couchdb bulk write: %w", res.Error)
		}
		if _, existed := revs[batch[i].Key]; existed && batch[i].Key != "" {
			stat.Overwritten++
		} else {
			stat.Written++
		}
	}
	return stat, nil
}

// insertOnly inserts new documents and counts conflicts (an _id that already exists)
// as skips. CouchDB reports each collision as a per-document 409 in the bulk result,
// so the batch continues past a collision; any other per-document error fails it.
func (s *Store) insertOnly(ctx context.Context, batch []query.Record) (query.WriteStat, error) {
	docs := make([]any, len(batch))
	for i, r := range batch {
		doc, err := documentBody(r)
		if err != nil {
			return query.WriteStat{}, err
		}
		if r.Key != "" {
			doc["_id"] = r.Key
		}
		docs[i] = doc
	}

	results, err := s.client.DB(s.db).BulkDocs(ctx, docs)
	if err != nil {
		return query.WriteStat{}, fmt.Errorf("couchdb bulk write: %w", err)
	}
	var stat query.WriteStat
	for _, res := range results {
		if res.Error != nil {
			if kivik.HTTPStatus(res.Error) == http.StatusConflict {
				stat.Skipped++
				continue
			}
			return query.WriteStat{}, fmt.Errorf("couchdb insert: %w", res.Error)
		}
		stat.Written++
	}
	return stat, nil
}

// currentRevs reads the current _rev of every keyed record that already exists, so
// an upsert can supply it. Keyless records are ignored (they are always fresh
// inserts). A key with no live document is simply absent from the result.
func (s *Store) currentRevs(ctx context.Context, batch []query.Record) (map[string]string, error) {
	keys := make([]string, 0, len(batch))
	for _, r := range batch {
		if r.Key != "" {
			keys = append(keys, r.Key)
		}
	}
	return s.currentRevsForKeys(ctx, keys)
}

// currentRevsForKeys reads the current _rev of every given key that has a live
// document, via one _all_docs request. A key with no live document is simply absent
// from the result. It is shared by Put's upsert (which supplies the rev) and Delete
// (which needs the rev to write a tombstone, and treats an absent key as Missing).
func (s *Store) currentRevsForKeys(ctx context.Context, keys []string) (map[string]string, error) {
	revs := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return revs, nil
	}
	rows := s.client.DB(s.db).AllDocs(ctx, kivik.Param("keys", keys))
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		id, err := rows.ID()
		if err != nil {
			// A not_found row carries no id; it is simply a key with no document.
			continue
		}
		rev := rowRev(rows)
		if rev == "" {
			continue
		}
		revs[id] = rev
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("couchdb read revisions: %w", err)
	}
	return revs, nil
}

// rowRev reads the current _rev from an _all_docs row. The rev lives in the row's
// value ({"rev":"…"}), which a rev-less _all_docs query does not surface through
// ResultSet.Rev(), so it is read from the value directly. A missing or unreadable
// value (a not_found or deleted row) yields "".
func rowRev(rows *kivik.ResultSet) string {
	var v struct {
		Rev string `json:"rev"`
	}
	if err := rows.ScanValue(&v); err != nil {
		return ""
	}
	return v.Rev
}

// documentBody builds the JSON document to write for a record, with any read-in
// _id/_rev stripped so identity is carried by the record key, not the value. The value
// must be a JSON object: a scalar is rejected rather than wrapped as {value: ...}, so a
// successful Put round-trips exactly.
func documentBody(r query.Record) (map[string]any, error) {
	obj, ok := r.Value.(map[string]any)
	if !ok {
		return nil, query.NonObjectValueError("couchdb", r.Key)
	}
	doc := map[string]any{}
	for k, v := range obj {
		if k == "_id" || k == "_rev" {
			continue // identity comes from the record key; the rev is set by the upsert.
		}
		doc[k] = v
	}
	return doc, nil
}

// Clear empties the database, keeping it and its design documents (the `iq data
// clear` semantics for a document store): it streams every non-design document as a
// deletion tombstone and bulk-deletes them in batches of pageSize. Design documents
// (Mango indexes, views) are left in place so indexes survive.
func (s *Store) Clear(ctx context.Context) error {
	if s.db == "" {
		return errNoDatabase
	}
	db := s.client.DB(s.db)
	return s.scanDeletes(ctx, func(batch []any) error {
		if _, err := db.BulkDocs(ctx, batch); err != nil {
			return fmt.Errorf("couchdb bulk delete: %w", err)
		}
		return nil
	})
}

// scanDeletes streams every non-design document's {_id, _rev, _deleted:true}
// tombstone from a single _all_docs request, handing fn each batch of pageSize (the
// last batch is the remainder). Clear deletes through it; keeping the paged walk in
// one place makes the batch boundary observable to a test (fn sees the batch sizes),
// so the flush thresholds are covered rather than equivalent mutants.
func (s *Store) scanDeletes(ctx context.Context, fn func(batch []any) error) error {
	rows := s.client.DB(s.db).AllDocs(ctx)
	defer func() { _ = rows.Close() }()

	batch := make([]any, 0, s.pageSize)
	for rows.Next() {
		id, err := rows.ID()
		if err != nil {
			return fmt.Errorf("couchdb row id: %w", err)
		}
		if id == "" || strings.HasPrefix(id, designPrefix) {
			continue
		}
		rev := rowRev(rows)
		if rev == "" {
			continue
		}
		batch = append(batch, map[string]any{"_id": id, "_rev": rev, "_deleted": true})
		if len(batch) >= s.pageSize {
			if err := fn(batch); err != nil {
				return err
			}
			batch = make([]any, 0, s.pageSize)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("couchdb all_docs: %w", err)
	}
	if len(batch) > 0 {
		return fn(batch)
	}
	return nil
}

// Drop removes the database entirely — documents and indexes (the `iq data drop`
// semantics). The Store implements Dropper because a CouchDB database is a removable
// container.
func (s *Store) Drop(ctx context.Context) error {
	if s.db == "" {
		return errNoDatabase
	}
	if err := s.client.DestroyDB(ctx, s.db); err != nil {
		return fmt.Errorf("couchdb destroy db: %w", err)
	}
	return nil
}

// Delete removes the named keys by writing a {_id, _rev, _deleted:true} tombstone for
// each key that has a live document, bulk-deleted in batches of pageSize (the same
// _bulk_docs primitive Clear uses). A key with no live document is never sent and
// counts as Missing — no fetch, no error — so a re-run is idempotent. Deleted counts
// the tombstones CouchDB accepted; a per-document failure (a stale-rev conflict under
// a concurrent write) is a returned error, not a silent miscount.
func (s *Store) Delete(ctx context.Context, keys []string) (query.DeleteStat, error) {
	if s.db == "" {
		return query.DeleteStat{}, errNoDatabase
	}
	revs, err := s.currentRevsForKeys(ctx, keys)
	if err != nil {
		return query.DeleteStat{}, err
	}
	var stat query.DeleteStat
	docs := make([]any, 0, len(keys))
	for _, k := range keys {
		rev, ok := revs[k]
		if !ok {
			stat.Missing++
			continue
		}
		docs = append(docs, map[string]any{"_id": k, "_rev": rev, "_deleted": true})
	}
	db := s.client.DB(s.db)
	for chunk := range slices.Chunk(docs, s.pageSize) {
		results, err := db.BulkDocs(ctx, chunk)
		if err != nil {
			return query.DeleteStat{}, fmt.Errorf("couchdb bulk delete: %w", err)
		}
		for _, r := range results {
			if r.Error != nil {
				return query.DeleteStat{}, fmt.Errorf("couchdb delete %q: %w", r.ID, r.Error)
			}
			stat.Deleted++
		}
	}
	return stat, nil
}

// TypedScan streams the whole database as typed records, reusing the ScanBatches
// walk. Every item is a document, so the type tag is "document"; the key is the _id
// and the value is the normalized document.
func (s *Store) TypedScan(ctx context.Context, fn func(batch []query.Record) error) error {
	return s.ScanBatches(ctx, func(batch map[string]any) error {
		recs := make([]query.Record, 0, len(batch))
		for k, v := range batch {
			recs = append(recs, query.Record{Key: k, Type: "document", Value: v})
		}
		return fn(recs)
	})
}

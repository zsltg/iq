package couchbase

import (
	"context"
	"errors"
	"fmt"

	"github.com/couchbase/gocb/v2"
	"github.com/google/uuid"

	"github.com/zsltg/iq/internal/query"
)

// putItem is one record resolved for writing: its document body and the key it writes
// under, with keyed recording whether the key came from the record (an existing key,
// eligible to be overwritten) or was minted for a keyless record (always a fresh
// insert).
type putItem struct {
	key   string
	keyed bool
	doc   map[string]any
}

// Put writes a batch of records into the collection, keyed by document ID. Upsert
// overwrites an existing document and inserts a new one; InsertOnly inserts new
// documents and skips (counts) those whose ID already exists. A keyless record mints a
// client-side UUIDv4 key (foreign JSON in, like Mongo's _id). Every document is built
// before the bulk call, so a record that cannot be represented — a non-object value —
// fails the whole batch before any write lands. Every value is JSON-encoded by the
// SDK, never string-built.
func (s *Store) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	if s.collection == nil {
		return query.WriteStat{}, errNoBucket
	}
	if len(batch) == 0 {
		return query.WriteStat{}, nil
	}
	items := make([]putItem, len(batch))
	for i, r := range batch {
		doc, err := documentBody(r)
		if err != nil {
			return query.WriteStat{}, err
		}
		key, keyed := r.Key, r.Key != ""
		if keyed {
			if err := validateKey(key); err != nil {
				return query.WriteStat{}, err
			}
		} else {
			key = uuid.NewString()
		}
		items[i] = putItem{key: key, keyed: keyed, doc: doc}
	}
	if mode == query.InsertOnly {
		return s.insertOnly(ctx, items)
	}
	return s.upsert(ctx, items)
}

// documentBody returns the JSON document to write for a record. The value must be a
// JSON object: a scalar or array is rejected rather than wrapped as {value: …}, so a
// successful Put round-trips exactly (the uniform keyed-store contract).
func documentBody(r query.Record) (map[string]any, error) {
	obj, ok := r.Value.(map[string]any)
	if !ok {
		return nil, query.NonObjectValueError("couchbase", r.Key)
	}
	return obj, nil
}

// upsert overwrites each record's document by ID, creating it when absent. It first
// reads which keyed IDs already exist (a KV pre-read, accounting only), bulk-upserts
// every document, then counts a pre-existing key as overwritten and the rest as
// written. The pre-read is not atomic with the writes, so the overwrite count can skew
// by one under a concurrent write to the same key — accounting, never correctness.
func (s *Store) upsert(ctx context.Context, items []putItem) (query.WriteStat, error) {
	keyed := make([]string, 0, len(items))
	for _, it := range items {
		if it.keyed {
			keyed = append(keyed, it.key)
		}
	}
	existing, err := s.existingKeys(ctx, keyed)
	if err != nil {
		return query.WriteStat{}, err
	}

	ops := make([]gocb.BulkOp, len(items))
	upserts := make([]*gocb.UpsertOp, len(items))
	for i, it := range items {
		op := &gocb.UpsertOp{ID: it.key, Value: it.doc}
		upserts[i] = op
		ops[i] = op
	}
	s.tracef("upsert %d documents", len(items))
	if err := s.collection.Do(ops, &gocb.BulkOpOptions{Context: ctx}); err != nil {
		return query.WriteStat{}, fmt.Errorf("couchbase upsert: %w", err)
	}
	var stat query.WriteStat
	for i, op := range upserts {
		if op.Err != nil {
			return query.WriteStat{}, fmt.Errorf("couchbase upsert: %w", op.Err)
		}
		if _, ok := existing[items[i].key]; ok {
			stat.Overwritten++
		} else {
			stat.Written++
		}
	}
	return stat, nil
}

// insertOnly inserts new documents and counts an ID that already exists as a skip. Each
// collision surfaces as a per-document ErrDocumentExists in the bulk result, so the
// batch continues past a collision; any other per-document error fails it.
func (s *Store) insertOnly(ctx context.Context, items []putItem) (query.WriteStat, error) {
	ops := make([]gocb.BulkOp, len(items))
	inserts := make([]*gocb.InsertOp, len(items))
	for i, it := range items {
		op := &gocb.InsertOp{ID: it.key, Value: it.doc}
		inserts[i] = op
		ops[i] = op
	}
	s.tracef("insert %d documents", len(items))
	if err := s.collection.Do(ops, &gocb.BulkOpOptions{Context: ctx}); err != nil {
		return query.WriteStat{}, fmt.Errorf("couchbase insert: %w", err)
	}
	var stat query.WriteStat
	for _, op := range inserts {
		if op.Err != nil {
			if errors.Is(op.Err, gocb.ErrDocumentExists) {
				stat.Skipped++
				continue
			}
			return query.WriteStat{}, fmt.Errorf("couchbase insert: %w", op.Err)
		}
		stat.Written++
	}
	return stat, nil
}

// existingKeys returns which of keys currently have a document, via one KV bulk get.
// It reads only existence (the content is ignored), so an upsert can count overwrites
// without a query pass. A key with no document is simply absent from the result.
func (s *Store) existingKeys(ctx context.Context, keys []string) (map[string]struct{}, error) {
	exist := make(map[string]struct{}, len(keys))
	if len(keys) == 0 {
		return exist, nil
	}
	ops := make([]gocb.BulkOp, 0, len(keys))
	getOps := make([]*gocb.GetOp, 0, len(keys))
	for _, k := range keys {
		op := &gocb.GetOp{ID: k}
		getOps = append(getOps, op)
		ops = append(ops, op)
	}
	if err := s.collection.Do(ops, &gocb.BulkOpOptions{Context: ctx}); err != nil {
		return nil, fmt.Errorf("couchbase pre-read: %w", err)
	}
	for _, op := range getOps {
		switch {
		case op.Err == nil:
			exist[op.ID] = struct{}{}
		case errors.Is(op.Err, gocb.ErrDocumentNotFound):
			// Absent: not an overwrite, and not an error.
		default:
			return nil, fmt.Errorf("couchbase pre-read: %w", op.Err)
		}
	}
	return exist, nil
}

// Clear empties the collection, keeping it (the `iq data clear` semantics): a
// parameterless DELETE over the keyspace removes every document. It is a query pass,
// so it needs an index on the collection, the same operational constraint the scan
// paths carry.
func (s *Store) Clear(ctx context.Context) error {
	if s.collection == nil {
		return errNoBucket
	}
	stmt := fmt.Sprintf("DELETE FROM %s", s.keyspaceRef())
	s.tracef("query %s", stmt)
	rows, err := s.query(ctx, stmt, nil)
	if err != nil {
		return s.queryError("couchbase clear", err)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return s.queryError("couchbase clear", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("couchbase clear: %w", err)
	}
	return nil
}

// Drop removes the collection entirely (the `iq data drop` semantics). The default
// collection cannot be dropped server-side, so that case returns a clear hint to use
// clear instead of surfacing a raw server error.
func (s *Store) Drop(ctx context.Context) error {
	if s.collection == nil {
		return errNoBucket
	}
	if s.scope == defaultScope && s.coll == defaultCollection {
		return fmt.Errorf("couchbase drop: the default collection cannot be dropped; use clear to empty it")
	}
	mgr := s.cluster.Bucket(s.bucket).Collections()
	s.tracef("drop collection %s.%s", s.scope, s.coll)
	err := mgr.DropCollection(gocb.CollectionSpec{Name: s.coll, ScopeName: s.scope}, &gocb.DropCollectionOptions{Context: ctx})
	if err != nil {
		return fmt.Errorf("couchbase drop: %w", err)
	}
	return nil
}

// TypedScan streams the whole collection as typed records, reusing the ScanBatches
// walk. Every item is a document, so the type tag is "document"; the key is the
// document ID and the value is the normalized document.
func (s *Store) TypedScan(ctx context.Context, fn func(batch []query.Record) error) error {
	return s.ScanBatches(ctx, func(batch map[string]any) error {
		recs := make([]query.Record, 0, len(batch))
		for k, v := range batch {
			recs = append(recs, query.Record{Key: k, Type: "document", Value: v})
		}
		return fn(recs)
	})
}

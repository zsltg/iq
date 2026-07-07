package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/zsltg/iq/internal/query"
)

// Put writes a batch of records into the index, keyed by _id, with a _bulk request
// that refreshes so the writes are immediately searchable. Upsert indexes each record
// (replacing an existing document by _id); InsertOnly creates each and skips (counts)
// those whose _id already exists, reported by Elasticsearch as a per-item 409
// conflict. A keyless record lets Elasticsearch mint the _id. Every value is
// marshalled as JSON by the driver — never string-built.
func (s *Store) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	if s.index == "" {
		return query.WriteStat{}, errNoIndex
	}
	if len(batch) == 0 {
		return query.WriteStat{}, nil
	}
	action := "index"
	if mode == query.InsertOnly {
		action = "create"
	}

	var buf bytes.Buffer
	for _, r := range batch {
		meta := map[string]any{}
		if r.Key != "" {
			meta["_id"] = r.Key
		}
		if err := writeBulkLine(&buf, map[string]any{action: meta}); err != nil {
			return query.WriteStat{}, err
		}
		if err := writeBulkLine(&buf, documentBody(r)); err != nil {
			return query.WriteStat{}, err
		}
	}

	var br struct {
		Items []map[string]bulkItem `json:"items"`
	}
	res, err := s.es.Bulk(
		bytes.NewReader(buf.Bytes()),
		s.es.Bulk.WithIndex(s.index),
		s.es.Bulk.WithRefresh("true"),
		s.es.Bulk.WithContext(ctx),
	)
	if err := finish(res, err, "bulk write", &br); err != nil {
		return query.WriteStat{}, err
	}
	return tallyBulk(br.Items)
}

// bulkItem is one action's outcome in a _bulk response: the operation result
// ("created"/"updated"), the HTTP-style status, and an error when it failed.
type bulkItem struct {
	Result string     `json:"result"`
	Status int        `json:"status"`
	Error  *bulkError `json:"error"`
}

// bulkError is the per-item error an unsuccessful _bulk action reports.
type bulkError struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// tallyBulk reduces a _bulk response's per-item outcomes to a write stat: a 409 is a
// skipped insert-only conflict, any other error fails the whole batch, an "updated"
// result is an overwrite, and everything else (a "created" or other success) is a new
// write. Each item map carries exactly one action, so the inner loop runs once.
func tallyBulk(items []map[string]bulkItem) (query.WriteStat, error) {
	var stat query.WriteStat
	for _, item := range items {
		for _, r := range item {
			switch {
			case r.Error != nil && r.Status == 409:
				stat.Skipped++ // an existing _id under InsertOnly (create conflict).
			case r.Error != nil:
				return query.WriteStat{}, fmt.Errorf("elasticsearch bulk write: %s: %s", r.Error.Type, r.Error.Reason)
			case r.Result == "updated":
				stat.Overwritten++
			default: // "created", or any non-error result.
				stat.Written++
			}
		}
	}
	return stat, nil
}

// writeBulkLine appends one newline-delimited JSON object to a _bulk request buffer.
func writeBulkLine(buf *bytes.Buffer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode elasticsearch bulk line: %w", err)
	}
	buf.Write(b)
	buf.WriteByte('\n')
	return nil
}

// documentBody builds the JSON document to write for a record: the value when it is
// an object, else a {value: ...} wrapper, with the injected _id stripped so identity
// is carried by the record key, not the value (Elasticsearch reserves _id as a
// metadata field and rejects it inside _source).
func documentBody(r query.Record) map[string]any {
	doc := map[string]any{}
	if obj, ok := r.Value.(map[string]any); ok {
		for k, v := range obj {
			if k == "_id" {
				continue // identity comes from the record key.
			}
			doc[k] = v
		}
		return doc
	}
	doc["value"] = r.Value
	return doc
}

// Clear empties the index, keeping it and its mapping/settings (the `iq data clear`
// semantics for a document store): it deletes every document with a match-all
// _delete_by_query and refreshes so the emptiness is immediately visible.
func (s *Store) Clear(ctx context.Context) error {
	if s.index == "" {
		return errNoIndex
	}
	body, err := jsonReader(map[string]any{"query": map[string]any{"match_all": map[string]any{}}})
	if err != nil {
		return err
	}
	res, err := s.es.DeleteByQuery(
		[]string{s.index},
		body,
		s.es.DeleteByQuery.WithRefresh(true),
		s.es.DeleteByQuery.WithContext(ctx),
	)
	return finish(res, err, "delete by query", nil)
}

// Drop removes the index entirely — documents, mapping, and settings (the `iq data
// drop` semantics). The Store implements Dropper because an Elasticsearch index is a
// removable container.
func (s *Store) Drop(ctx context.Context) error {
	if s.index == "" {
		return errNoIndex
	}
	res, err := s.es.Indices.Delete([]string{s.index}, s.es.Indices.Delete.WithContext(ctx))
	return finish(res, err, "delete index", nil)
}

// TypedScan streams the whole index as typed records, reusing the ScanBatches walk.
// Every item is a document, so the type tag is "document"; the key is the _id and the
// value is the normalized document.
func (s *Store) TypedScan(ctx context.Context, fn func(batch []query.Record) error) error {
	return s.ScanBatches(ctx, func(batch map[string]any) error {
		recs := make([]query.Record, 0, len(batch))
		for k, v := range batch {
			recs = append(recs, query.Record{Key: k, Type: "document", Value: v})
		}
		return fn(recs)
	})
}

// compile-time assertions that Store satisfies the optional write capabilities.
var (
	_ query.Putter      = (*Store)(nil)
	_ query.Clearer     = (*Store)(nil)
	_ query.Dropper     = (*Store)(nil)
	_ query.TypedReader = (*Store)(nil)
)

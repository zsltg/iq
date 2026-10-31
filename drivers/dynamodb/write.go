package dynamodb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/zsltg/iq/internal/query"
)

// Put writes a batch of records into the table, keyed by primary key. Upsert
// issues a plain PutItem, which DynamoDB applies as an overwrite-or-insert.
// InsertOnly issues a PutItem with a condition that the partition key not exist,
// and counts a record whose key exists as a skip. Every value is marshaled to a
// typed attribute. A record without a key attribute, or with an unrepresentable
// value, is an error, never a silent drop. Every item is built before any is
// written, so a bad record fails the whole batch before it changes the table. The
// PutItem reply cannot tell an insert from an overwrite, so an upsert pre-reads
// which keys exist to report Overwritten. See existingKeys for the caveat.
func (s *Store) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	if len(s.keys) == 0 {
		return query.WriteStat{}, errNoTable
	}
	items, err := s.itemsOf(batch)
	if err != nil {
		return query.WriteStat{}, err
	}
	existing, err := s.existingFor(ctx, batch, mode)
	if err != nil {
		return query.WriteStat{}, err
	}
	var stat query.WriteStat
	for i, r := range batch {
		skipped, err := s.putItem(ctx, items[i], mode)
		if err != nil {
			return query.WriteStat{}, err
		}
		switch {
		case skipped:
			stat.Skipped++
		case existing[r.Key]:
			stat.Overwritten++
		default:
			stat.Written++
		}
	}
	return stat, nil
}

// itemsOf builds the DynamoDB item of every record before any write, so one bad
// record fails the whole batch.
func (s *Store) itemsOf(batch []query.Record) ([]map[string]types.AttributeValue, error) {
	items := make([]map[string]types.AttributeValue, len(batch))
	for i, r := range batch {
		// The SDK writes S and the names as JSON, which turns an invalid byte into U+FFFD.
		if err := query.CheckKeyUTF8("dynamodb", r.Key); err != nil {
			return nil, err
		}
		if err := query.CheckValueUTF8("dynamodb", r.Key, r.Value); err != nil {
			return nil, err
		}
		item, err := s.toItem(recordValue{key: r.Key, value: r.Value})
		if err != nil {
			return nil, err
		}
		items[i] = item
	}
	return items, nil
}

// existingFor returns which keys of batch already hold an item when mode is Upsert,
// for the Overwritten count. Any other mode needs no pre-read and gets a nil map.
func (s *Store) existingFor(ctx context.Context, batch []query.Record, mode query.WriteMode) (map[string]bool, error) {
	if mode != query.Upsert {
		return nil, nil
	}
	return s.existingKeys(ctx, batch)
}

// putItem writes one item. In InsertOnly mode the partition key must not exist yet,
// and a key that does exist is not an error: it reports skipped as true.
func (s *Store) putItem(ctx context.Context, item map[string]types.AttributeValue, mode query.WriteMode) (skipped bool, err error) {
	in := &dynamodb.PutItemInput{TableName: &s.table, Item: item}
	if mode == query.InsertOnly {
		// Skip a key that already exists: the partition key must not be present.
		// Aliased through #pk so a reserved partition-key name is still safe.
		in.ConditionExpression = aws.String("attribute_not_exists(#pk)")
		in.ExpressionAttributeNames = map[string]string{"#pk": s.keys[0].name}
	}
	if _, err := s.client.PutItem(ctx, in); err != nil {
		var ccf *types.ConditionalCheckFailedException
		if mode == query.InsertOnly && errors.As(err, &ccf) {
			return true, nil
		}
		return false, fmt.Errorf("dynamodb put: %w", err)
	}
	return false, nil
}

// existingKeys returns which of a batch's keys already have an item, so an upsert
// can report them as Overwritten. It reads the distinct keys of the batch with a
// key-only projected BatchGetItem. This is the same drain loop as Get, but the
// response carries only key attributes, never values. The read is accounting only.
// It never changes what Put writes. It is not atomic with the writes that follow,
// so a concurrent insert between the two can skew the count by one. The items
// written are always correct.
func (s *Store) existingKeys(ctx context.Context, batch []query.Record) (map[string]bool, error) {
	seen := make(map[string]bool, len(batch))
	keys := make([]string, 0, len(batch))
	for _, r := range batch {
		// A keyless record has no key to pre-read. DynamoDB requires one, so toItem has
		// already rejected it. Deduplicate, because BatchGetItem rejects a request with
		// duplicate keys, and a repeated key is present-or-absent exactly once
		// regardless.
		if r.Key == "" || seen[r.Key] {
			continue
		}
		seen[r.Key] = true
		keys = append(keys, r.Key)
	}
	return s.existingKeySet(ctx, keys)
}

// existingKeySet returns which of the given distinct keys already have an item. It
// reads with the same key-only projected BatchGetItem drain as existingKeys. It is
// the accounting pre-read shared by Put's overwrite count and Delete's
// present-vs-absent count. The same non-atomic caveat applies. Callers pass keys
// already deduped.
func (s *Store) existingKeySet(ctx context.Context, keys []string) (map[string]bool, error) {
	proj, names := s.keyProjection()
	existing := make(map[string]bool, len(keys))
	for start := 0; start < len(keys); start += batchGetMax {
		end := min(start+batchGetMax, len(keys))
		reqKeys, err := s.decodeKeys(keys[start:end])
		if err != nil {
			return nil, err
		}
		pending := map[string]types.KeysAndAttributes{s.table: {
			Keys:                     reqKeys,
			ProjectionExpression:     aws.String(proj),
			ExpressionAttributeNames: names,
		}}
		if err := s.drainBatchGet(ctx, pending, func(item map[string]types.AttributeValue) {
			existing[s.keyOf(item)] = true
		}); err != nil {
			return nil, err
		}
	}
	return existing, nil
}

// Clear empties the table and keeps its schema (the `iq data clear` semantics). It
// scans every item's primary key and deletes the items with BatchWriteItem.
// DynamoDB has no TRUNCATE, and recreating the table loses its indexes,
// throughput, and other settings. So the table definition is kept and only its
// items are removed. This consumes read and write capacity proportional to the item
// count, which `--explain` surfaces.
func (s *Store) Clear(ctx context.Context) error {
	if len(s.keys) == 0 {
		return errNoTable
	}
	proj, names := s.keyProjection()
	in := &dynamodb.ScanInput{
		TableName:                &s.table,
		ProjectionExpression:     aws.String(proj),
		ExpressionAttributeNames: names,
	}
	p := dynamodb.NewScanPaginator(s.client, in)
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("dynamodb scan: %w", err)
		}
		if err := s.deleteItems(ctx, out.Items); err != nil {
			return err
		}
	}
	return nil
}

// keyProjection builds a projection expression naming only the primary-key attributes,
// each aliased through a #kN placeholder so a reserved key-attribute name is safe. Clear
// scans with it so each page carries only the keys it needs to delete, not whole items.
func (s *Store) keyProjection() (string, map[string]string) {
	names := make(map[string]string, len(s.keys))
	parts := make([]string, len(s.keys))
	for i, k := range s.keys {
		ph := fmt.Sprintf("#k%d", i)
		names[ph] = k.name
		parts[i] = ph
	}
	var proj strings.Builder
	proj.WriteString(parts[0])
	for _, p := range parts[1:] {
		proj.WriteString(", " + p)
	}
	return proj.String(), names
}

// deleteItems removes a page of items by primary key with BatchWriteItem in batches of
// 25 (DynamoDB's limit), retrying the throttled leftovers (UnprocessedItems) with
// bounded backoff. Each delete request carries only the item's key attributes.
func (s *Store) deleteItems(ctx context.Context, items []map[string]types.AttributeValue) error {
	for start := 0; start < len(items); start += batchWriteMax {
		end := min(start+batchWriteMax, len(items))
		pending := map[string][]types.WriteRequest{s.table: s.deleteRequests(items[start:end])}
		if err := s.drainBatchWrite(ctx, pending); err != nil {
			return err
		}
	}
	return nil
}

// deleteRequests builds one delete request per item, each with only its key attributes.
func (s *Store) deleteRequests(items []map[string]types.AttributeValue) []types.WriteRequest {
	reqs := make([]types.WriteRequest, 0, len(items))
	for _, item := range items {
		reqs = append(reqs, types.WriteRequest{
			DeleteRequest: &types.DeleteRequest{Key: s.keyAttrs(item)},
		})
	}
	return reqs
}

// drainBatchWrite issues BatchWriteItem against pending and drains the throttled
// leftovers (UnprocessedItems) with bounded backoff between attempts. The range bounds
// the retries, so it can never loop forever.
func (s *Store) drainBatchWrite(ctx context.Context, pending map[string][]types.WriteRequest) error {
	for attempt := range maxUnprocessed {
		out, err := s.client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: pending})
		if err != nil {
			return fmt.Errorf("dynamodb batch write: %w", err)
		}
		pending = out.UnprocessedItems
		if len(pending) == 0 {
			return nil
		}
		if err := backoff(ctx, attempt); err != nil {
			return err
		}
	}
	return fmt.Errorf("dynamodb batch write: %d attempt(s) left items unprocessed", maxUnprocessed)
}

// keyAttrs extracts only the primary-key attributes from an item: the key that a
// delete or point operation needs.
func (s *Store) keyAttrs(item map[string]types.AttributeValue) map[string]types.AttributeValue {
	key := make(map[string]types.AttributeValue, len(s.keys))
	for _, k := range s.keys {
		if av, ok := item[k.name]; ok {
			key[k.name] = av
		}
	}
	return key
}

// Delete removes the named keys. It decodes each key to its key attributes (a
// single hash key is the bare string, a hash+sort key a JSON array) and calls
// deleteItems: BatchWriteItem in batches of 25 with bounded UnprocessedItems retry.
// BatchWriteItem does not say whether an item existed, so a pre-read
// (existingKeySet) gives the present and absent counts. The delete requests run
// regardless, so Delete is idempotent.
func (s *Store) Delete(ctx context.Context, keys []string) (query.DeleteStat, error) {
	if len(s.keys) == 0 {
		return query.DeleteStat{}, errNoTable
	}
	// The SDK writes each key as JSON, which turns an invalid byte into U+FFFD.
	if err := query.CheckKeysUTF8("dynamodb", keys); err != nil {
		return query.DeleteStat{}, err
	}
	existing, err := s.existingKeySet(ctx, keys)
	if err != nil {
		return query.DeleteStat{}, err
	}
	items, err := s.decodeKeys(keys)
	if err != nil {
		return query.DeleteStat{}, err
	}
	if err := s.deleteItems(ctx, items); err != nil {
		return query.DeleteStat{}, err
	}
	var stat query.DeleteStat
	for _, k := range keys {
		if existing[k] {
			stat.Deleted++
		} else {
			stat.Missing++
		}
	}
	return stat, nil
}

// Drop removes the table entirely: its items and schema (the `iq data drop`
// semantics). It uses DeleteTable. The Store implements Dropper because a DynamoDB
// table is a removable container, like a Cassandra table. DeleteTable is
// asynchronous. DynamoDB accepts the request and removes the table in the
// background. So this returns once the request is accepted, which matches the
// fire-and-forget shape of DROP TABLE.
func (s *Store) Drop(ctx context.Context) error {
	if len(s.keys) == 0 {
		return errNoTable
	}
	if _, err := s.client.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: &s.table}); err != nil {
		return fmt.Errorf("dynamodb delete table: %w", err)
	}
	return nil
}

// TypedScan streams the whole table as typed records and reuses the ScanBatches
// pager. Every item is a document, so the type tag is "item". The key is the
// encoded primary key, and the value is the normalized item.
func (s *Store) TypedScan(ctx context.Context, fn func(batch []query.Record) error) error {
	return s.ScanBatches(ctx, func(batch map[string]any) error {
		recs := make([]query.Record, 0, len(batch))
		for k, v := range batch {
			recs = append(recs, query.Record{Key: k, Type: "item", Value: v})
		}
		return fn(recs)
	})
}

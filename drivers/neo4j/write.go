package neo4j

import (
	"context"
	"errors"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/zsltg/iq/internal/query"
)

// errWriteNeedsKey is returned when a write runs on a source with no ?key=. A node's
// elementId is server-assigned, so it cannot be a MERGE key; an upsert needs a
// stable property key to reconcile a record against an existing node.
var errWriteNeedsKey = errors.New("neo4j: writing needs a key property; set ?key= in the source url so nodes upsert on a stable key")

// reserved is the set of value keys iq injects that are not real node properties, so
// a write strips them before MERGE — writing _id back would try to set a property
// named _id from the read envelope.
var reserved = map[string]struct{}{
	fieldID: {}, fieldLabels: {}, "_type": {}, "_start": {}, "_end": {},
}

// Put upserts a batch of nodes, MERGE-ing each on the key property so a re-run
// converges. Upsert sets every matched node's properties; InsertOnly sets only newly
// created nodes and counts the rest skipped. It requires a ?key= source and a
// uniqueness constraint behind it: without the constraint a MERGE could match — and
// then set — several nodes at once, so the write is refused up front rather than
// silently fanning out.
func (s *Store) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	var stat query.WriteStat
	if s.target.label == "" {
		return stat, errNoLabel
	}
	if s.target.key == "" {
		return stat, errWriteNeedsKey
	}
	if !s.keyBackedByConstraint {
		return stat, fmt.Errorf("neo4j: %s.%s has no uniqueness constraint; create one (CREATE CONSTRAINT ... FOR (n:%s) REQUIRE n.%s IS UNIQUE) before writing so an upsert cannot fan out across duplicate nodes",
			s.target.label, s.target.key, s.target.label, s.target.key)
	}
	if len(batch) == 0 {
		return stat, nil
	}

	rows := make([]any, 0, len(batch))
	for _, rec := range batch {
		props, keyVal, err := recordProps(rec, s.target.key)
		if err != nil {
			return stat, err
		}
		rows = append(rows, map[string]any{"k": keyVal, "props": props})
	}

	sess := s.session(ctx, neo4j.AccessModeWrite)
	defer func() { _ = sess.Close(ctx) }()

	merge := "UNWIND $rows AS row MERGE (n:`" + s.target.label + "` {`" + s.target.key + "`: row.k}) "
	cypher := merge + "SET n += row.props"
	if mode == query.InsertOnly {
		cypher = merge + "ON CREATE SET n += row.props"
	}
	res, err := s.run(ctx, sess, cypher, map[string]any{"rows": rows})
	if err != nil {
		return stat, err
	}
	summary, err := res.Consume(ctx)
	if err != nil {
		return stat, fmt.Errorf("neo4j put: %w", err)
	}

	created := summary.Counters().NodesCreated()
	stat.Written = created
	if mode == query.InsertOnly {
		stat.Skipped = len(batch) - created
	} else {
		stat.Overwritten = len(batch) - created
	}
	return stat, nil
}

// recordProps derives the property map and merge-key value for one write record. The
// value must be a JSON object (a node's properties); iq's reserved envelope keys are
// stripped. The merge key is the record value's own key property when present (so its
// type is preserved), else the record key string, and it is always written onto the
// node so the stored node carries its key.
func recordProps(rec query.Record, key string) (map[string]any, any, error) {
	obj, ok := rec.Value.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("neo4j: node value for key %q must be a JSON object, got %T", rec.Key, rec.Value)
	}
	props := make(map[string]any, len(obj))
	for k, v := range obj {
		if _, skip := reserved[k]; skip {
			continue
		}
		props[k] = v
	}
	keyVal, ok := props[key]
	if !ok || keyVal == nil {
		keyVal = rec.Key
	}
	props[key] = keyVal
	return props, keyVal, nil
}

// Clear empties the label, detach-deleting every node that carries it (and, because
// of DETACH, any relationships those nodes hold). It pages the delete so an arbitrarily
// large label does not run as one unbounded transaction. The label itself is not a
// removable object, so this is the closest operation to a drop.
func (s *Store) Clear(ctx context.Context) error {
	if s.target.label == "" {
		return errNoLabel
	}
	sess := s.session(ctx, neo4j.AccessModeWrite)
	defer func() { _ = sess.Close(ctx) }()

	cypher := "MATCH (n:`" + s.target.label + "`) WITH n LIMIT $limit DETACH DELETE n"
	for {
		res, err := s.run(ctx, sess, cypher, map[string]any{"limit": scanBatch})
		if err != nil {
			return err
		}
		summary, err := res.Consume(ctx)
		if err != nil {
			return fmt.Errorf("neo4j clear: %w", err)
		}
		if summary.Counters().NodesDeleted() == 0 {
			return nil
		}
	}
}

// TypedScan walks the label and hands the caller each page as records tagged
// "node", so a copy reconstructs each node. It reuses ScanBatches, so the key and
// value are exactly what the read path produces.
func (s *Store) TypedScan(ctx context.Context, fn func(batch []query.Record) error) error {
	return s.ScanBatches(ctx, func(page map[string]any) error {
		recs := make([]query.Record, 0, len(page))
		for k, v := range page {
			recs = append(recs, query.Record{Key: k, Type: "node", Value: v})
		}
		return fn(recs)
	})
}

// compile-time assertions that Store satisfies the optional write capabilities.
var (
	_ query.Putter      = (*Store)(nil)
	_ query.Clearer     = (*Store)(nil)
	_ query.TypedReader = (*Store)(nil)
)

package neo4j

import (
	"context"
	"errors"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/zsltg/iq/internal/neo4jenvelope"
	"github.com/zsltg/iq/internal/query"
)

// errWriteNeedsKey is returned when a write runs on a source with no ?key=. A node's
// elementId is server-assigned, so it cannot be a MERGE key; an upsert needs a
// stable property key to reconcile a record against an existing node.
var errWriteNeedsKey = errors.New("neo4j: writing needs a key property; set ?key= in the source url so nodes upsert on a stable key")

// errRelWriteUnsupported marks the deferred relationship-write path. Reading and
// copying relationships out works; creating them needs endpoint resolution (which
// nodes to connect, by which key) that is a further follow-up, so a write into a
// relationship collection is refused rather than half-done.
var errRelWriteUnsupported = errors.New("neo4j: writing relationships is not yet supported; a relationship source (?rel= / handle.:TYPE) is read-only — write nodes with ?label=")

// Put upserts a batch of nodes, MERGE-ing each on the key property so a re-run
// converges. Upsert sets every matched node's properties; InsertOnly sets only newly
// created nodes and counts the rest skipped. It requires a ?key= source and a
// uniqueness constraint behind it: without the constraint a MERGE could match — and
// then set — several nodes at once, so the write is refused up front rather than
// silently fanning out.
func (s *Store) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	var stat query.WriteStat
	if s.target.kind == relTarget {
		return stat, errRelWriteUnsupported
	}
	if s.target.name == "" {
		return stat, errNoLabel
	}
	if s.target.key == "" {
		return stat, errWriteNeedsKey
	}
	if !s.keyBackedByConstraint {
		return stat, fmt.Errorf("neo4j: %s.%s has no uniqueness constraint; create one (CREATE CONSTRAINT ... FOR (n:%s) REQUIRE n.%s IS UNIQUE) before writing so an upsert cannot fan out across duplicate nodes",
			s.target.name, s.target.key, s.target.name, s.target.key)
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

	merge := "UNWIND $rows AS row MERGE (n:`" + s.target.name + "` {`" + s.target.key + "`: row.k}) "
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
		if neo4jenvelope.IsReserved(k) {
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
	if s.target.kind == relTarget {
		return errRelWriteUnsupported
	}
	if s.target.name == "" {
		return errNoLabel
	}
	sess := s.session(ctx, neo4j.AccessModeWrite)
	defer func() { _ = sess.Close(ctx) }()

	cypher := "MATCH (n:`" + s.target.name + "`) WITH n LIMIT $limit DETACH DELETE n"
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

// Delete removes the named nodes, addressed exactly as Get addresses them: by
// elementId (the default) or by the ?key= property. It resolves each key to its
// matching node(s) first — refusing, like Get, a ?key= that matches more than one
// node, so a non-unique property key never over-deletes — then DETACH DELETEs the
// matched nodes by elementId (DETACH removes their relationships too). A key that
// matches no node is Missing, never an error, so a re-run is idempotent. Deleted and
// Missing are exact from the resolution; keys ride as bound parameters.
func (s *Store) Delete(ctx context.Context, keys []string) (query.DeleteStat, error) {
	if s.target.kind == relTarget {
		return query.DeleteStat{}, errRelWriteUnsupported
	}
	if s.target.name == "" {
		return query.DeleteStat{}, errNoLabel
	}
	if len(keys) == 0 {
		return query.DeleteStat{}, nil
	}
	sess := s.session(ctx, neo4j.AccessModeWrite)
	defer func() { _ = sess.Close(ctx) }()

	v := s.target.variable()
	params := map[string]any{"ids": keys}
	var matchExpr string
	if s.target.key == "" {
		matchExpr = "elementId(" + v + ")"
	} else {
		params["key"] = s.target.key
		matchExpr = "toString(" + v + "[$key])"
	}
	resolve := s.target.match() + " WHERE " + matchExpr + " IN $ids RETURN " + matchExpr + " AS k, elementId(" + v + ") AS eid"
	res, err := s.run(ctx, sess, resolve, params)
	if err != nil {
		return query.DeleteStat{}, err
	}
	matched := make(map[string]struct{}, len(keys))
	eids := make([]string, 0, len(keys))
	for res.Next(ctx) {
		rec := res.Record()
		k, _ := rec.Values[0].(string)
		eid, _ := rec.Values[1].(string)
		if _, dup := matched[k]; dup && s.target.key != "" {
			return query.DeleteStat{}, fmt.Errorf("neo4j: key %q matches more than one %s; %s.%s is not unique", k, s.target.noun(), s.target.name, s.target.key)
		}
		matched[k] = struct{}{}
		eids = append(eids, eid)
	}
	if err := res.Err(); err != nil {
		return query.DeleteStat{}, fmt.Errorf("neo4j delete resolve: %w", err)
	}

	// DETACH DELETE with an empty $eids matches nothing, so it runs unconditionally:
	// a guard on len(eids) would only be an equivalent mutant boundary, never a
	// behaviour change (the resolve above already recorded matched vs missing).
	del := s.target.match() + " WHERE elementId(" + v + ") IN $eids DETACH DELETE " + v
	dr, err := s.run(ctx, sess, del, map[string]any{"eids": eids})
	if err != nil {
		return query.DeleteStat{}, err
	}
	if _, err := dr.Consume(ctx); err != nil {
		return query.DeleteStat{}, fmt.Errorf("neo4j delete: %w", err)
	}
	return query.DeleteStat{Deleted: len(matched), Missing: len(keys) - len(matched)}, nil
}

// TypedScan walks the label and hands the caller each page as records tagged
// "node", so a copy reconstructs each node. It reuses ScanBatches, so the key and
// value are exactly what the read path produces.
func (s *Store) TypedScan(ctx context.Context, fn func(batch []query.Record) error) error {
	typ := s.target.noun() // "node" or "relationship"
	return s.ScanBatches(ctx, func(page map[string]any) error {
		recs := make([]query.Record, 0, len(page))
		for k, v := range page {
			recs = append(recs, query.Record{Key: k, Type: typ, Value: v})
		}
		return fn(recs)
	})
}

// compile-time assertions that Store satisfies the optional write capabilities.
var (
	_ query.Putter      = (*Store)(nil)
	_ query.Clearer     = (*Store)(nil)
	_ query.Deleter     = (*Store)(nil)
	_ query.TypedReader = (*Store)(nil)
)

package couchbase

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/couchbase/gocb/v2"
)

// InspectCluster reports the cluster's nodes and their services from the system:nodes
// catalog, the analogue of a server/build probe. It needs no bucket.
func (s *Store) InspectCluster(ctx context.Context) (any, error) {
	rows, err := s.cluster.Query("SELECT node.* FROM system:nodes AS node", &gocb.QueryOptions{Context: ctx})
	if err != nil {
		return nil, fmt.Errorf("couchbase inspect cluster: %w", err)
	}
	defer func() { _ = rows.Close() }()
	nodes := []any{}
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Row(&raw); err != nil {
			return nil, fmt.Errorf("couchbase inspect cluster: %w", err)
		}
		nodes = append(nodes, decodeValue(raw, s.decimal))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("couchbase inspect cluster: %w", err)
	}
	return map[string]any{"nodes": nodes}, nil
}

// InspectBuckets lists the cluster's buckets and their types, the analogue of listing
// the databases a server hosts. It needs no bucket selected.
func (s *Store) InspectBuckets(ctx context.Context) (any, error) {
	settings, err := s.cluster.Buckets().GetAllBuckets(&gocb.GetAllBucketsOptions{Context: ctx})
	if err != nil {
		return nil, fmt.Errorf("couchbase inspect buckets: %w", err)
	}
	names := make([]string, 0, len(settings))
	for name := range settings {
		names = append(names, name)
	}
	sort.Strings(names)
	buckets := make([]any, 0, len(names))
	for _, name := range names {
		buckets = append(buckets, map[string]any{
			"name":       name,
			"type":       string(settings[name].BucketType),
			"ramQuotaMB": settings[name].RAMQuotaMB,
		})
	}
	return map[string]any{"buckets": buckets}, nil
}

// InspectCollections lists the scopes and collections of the selected bucket, the
// analogue of describing a keyspace's tables. It needs a bucket selected.
func (s *Store) InspectCollections(ctx context.Context) (any, error) {
	if s.bucket == "" {
		return nil, errNoBucket
	}
	scopes, err := s.cluster.Bucket(s.bucket).Collections().GetAllScopes(&gocb.GetAllScopesOptions{Context: ctx})
	if err != nil {
		return nil, fmt.Errorf("couchbase inspect collections: %w", err)
	}
	out := make([]any, 0, len(scopes))
	for _, sc := range scopes {
		cols := make([]any, 0, len(sc.Collections))
		for _, col := range sc.Collections {
			cols = append(cols, col.Name)
		}
		out = append(out, map[string]any{"scope": sc.Name, "collections": cols})
	}
	return map[string]any{"bucket": s.bucket, "scopes": out}, nil
}

// InspectIndexes lists the query indexes on the selected bucket (or the whole cluster
// when none is selected) from the system:indexes catalog, so a user can see what a
// pushdown scan can use. Every value rides as a named parameter.
func (s *Store) InspectIndexes(ctx context.Context) (any, error) {
	stmt := "SELECT idx.* FROM system:indexes AS idx"
	opts := &gocb.QueryOptions{Context: ctx}
	if s.bucket != "" {
		stmt += " WHERE idx.bucket_id = $bucket OR (idx.bucket_id IS MISSING AND idx.keyspace_id = $bucket)"
		opts.NamedParameters = map[string]any{"bucket": s.bucket}
	}
	rows, err := s.cluster.Query(stmt, opts)
	if err != nil {
		return nil, fmt.Errorf("couchbase inspect indexes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	indexes := []any{}
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Row(&raw); err != nil {
			return nil, fmt.Errorf("couchbase inspect indexes: %w", err)
		}
		indexes = append(indexes, decodeValue(raw, s.decimal))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("couchbase inspect indexes: %w", err)
	}
	return map[string]any{"indexes": indexes}, nil
}

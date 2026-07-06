package couchdb

import (
	"context"
	"fmt"
)

// InspectServer returns the CouchDB server's version and vendor, the analogue of a
// buildInfo probe. It needs no database.
func (s *Store) InspectServer(ctx context.Context) (any, error) {
	v, err := s.client.Version(ctx)
	if err != nil {
		return nil, fmt.Errorf("couchdb server version: %w", err)
	}
	// Features is empty on CouchDB before 2.1; it is always reported (as []) so the
	// shape is stable across versions.
	features := make([]any, len(v.Features))
	for i, f := range v.Features {
		features[i] = f
	}
	return map[string]any{
		"version":  v.Version,
		"vendor":   v.Vendor,
		"features": features,
	}, nil
}

// InspectDatabases lists the databases the server hosts, the analogue of listing a
// keyspace's tables. It needs no database.
func (s *Store) InspectDatabases(ctx context.Context) (any, error) {
	names, err := s.client.AllDBs(ctx)
	if err != nil {
		return nil, fmt.Errorf("couchdb list databases: %w", err)
	}
	dbs := make([]any, len(names))
	for i, n := range names {
		dbs[i] = n
	}
	return map[string]any{"databases": dbs}, nil
}

// InspectDBInfo describes the selected database's size and sequence, the analogue of
// describing a collection's stats: its document and deleted counts, on-disk and data
// sizes, and update sequence. It needs a database selected.
func (s *Store) InspectDBInfo(ctx context.Context) (any, error) {
	if s.db == "" {
		return nil, errNoDatabase
	}
	stats, err := s.client.DB(s.db).Stats(ctx)
	if err != nil {
		return nil, fmt.Errorf("couchdb db info: %w", err)
	}
	return map[string]any{
		"name":         stats.Name,
		"docCount":     stats.DocCount,
		"deletedCount": stats.DeletedCount,
		"diskSize":     stats.DiskSize,
		"dataSize":     stats.ActiveSize,
		"updateSeq":    stats.UpdateSeq,
	}, nil
}

// InspectIndexes lists the database's Mango indexes, so a user can see what a _find
// pushdown can use. It needs a database selected.
func (s *Store) InspectIndexes(ctx context.Context) (any, error) {
	if s.db == "" {
		return nil, errNoDatabase
	}
	indexes, err := s.client.DB(s.db).GetIndexes(ctx)
	if err != nil {
		return nil, fmt.Errorf("couchdb list indexes: %w", err)
	}
	out := make([]any, len(indexes))
	for i, idx := range indexes {
		out[i] = map[string]any{
			"designDoc":  idx.DesignDoc,
			"name":       idx.Name,
			"type":       idx.Type,
			"definition": idx.Definition,
		}
	}
	return map[string]any{"indexes": out}, nil
}

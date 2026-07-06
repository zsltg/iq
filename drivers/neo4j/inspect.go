package neo4j

import (
	"context"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// InspectServer returns the Neo4j deployment's components — name, versions, and
// edition — the analogue of a server buildInfo probe. It needs no label.
func (s *Store) InspectServer(ctx context.Context) (any, error) {
	rows, err := s.readRows(ctx, "CALL dbms.components() YIELD name, versions, edition RETURN name, versions, edition")
	if err != nil {
		return nil, fmt.Errorf("neo4j server components: %w", err)
	}
	return rows, nil
}

// InspectDatabases lists the databases the deployment hosts, the analogue of listing
// a keyspace's tables. It needs no label.
func (s *Store) InspectDatabases(ctx context.Context) (any, error) {
	names, err := s.readColumn(ctx, "SHOW DATABASES YIELD name RETURN name")
	if err != nil {
		return nil, fmt.Errorf("neo4j list databases: %w", err)
	}
	return names, nil
}

// InspectLabels lists the node labels in the database — the addressable collections
// a source can target. It needs no label selected.
func (s *Store) InspectLabels(ctx context.Context) (any, error) {
	labels, err := s.readColumn(ctx, "CALL db.labels() YIELD label RETURN label")
	if err != nil {
		return nil, fmt.Errorf("neo4j list labels: %w", err)
	}
	return labels, nil
}

// InspectRelationshipTypes lists the relationship types in the database. They are not
// yet addressable as collections (a follow-up), but listing them here shows what a
// graph holds beyond its nodes.
func (s *Store) InspectRelationshipTypes(ctx context.Context) (any, error) {
	types, err := s.readColumn(ctx, "CALL db.relationshipTypes() YIELD relationshipType RETURN relationshipType")
	if err != nil {
		return nil, fmt.Errorf("neo4j list relationship types: %w", err)
	}
	return types, nil
}

// InspectConstraints lists the database's constraints, so a user can see which key
// property a ?key= source can safely upsert on (a uniqueness constraint) and what
// else the schema enforces. It needs no label selected.
func (s *Store) InspectConstraints(ctx context.Context) (any, error) {
	rows, err := s.readRows(ctx, "SHOW CONSTRAINTS YIELD name, type, entityType, labelsOrTypes, properties RETURN name, type, entityType, labelsOrTypes, properties")
	if err != nil {
		return nil, fmt.Errorf("neo4j list constraints: %w", err)
	}
	return rows, nil
}

// readRows runs a read-only Cypher statement and returns its rows as normalized
// {column: value} maps. It is the shared helper for the inspect reads, which are
// metadata procedures, not data scans.
func (s *Store) readRows(ctx context.Context, cypher string) ([]any, error) {
	sess := s.session(ctx, neo4j.AccessModeRead)
	defer func() { _ = sess.Close(ctx) }()

	res, err := s.run(ctx, sess, cypher, nil)
	if err != nil {
		return nil, err
	}
	rows := []any{}
	for res.Next(ctx) {
		rec := res.Record()
		row := make(map[string]any, len(rec.Keys))
		for i, k := range rec.Keys {
			row[k] = s.normalizeValue(rec.Values[i])
		}
		rows = append(rows, row)
	}
	if err := res.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

// readColumn runs a read-only single-column Cypher statement and returns the column
// values, the natural shape for a list of labels or database names.
func (s *Store) readColumn(ctx context.Context, cypher string) ([]any, error) {
	sess := s.session(ctx, neo4j.AccessModeRead)
	defer func() { _ = sess.Close(ctx) }()

	res, err := s.run(ctx, sess, cypher, nil)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for res.Next(ctx) {
		out = append(out, s.normalizeValue(res.Record().Values[0]))
	}
	if err := res.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

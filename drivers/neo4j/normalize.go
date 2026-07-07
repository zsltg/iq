// Package neo4j adapts a Neo4j property graph to the query ports. A node label is
// modelled as Map[key, node]: a node's key is the value of a chosen property
// (?key=) or its elementId, and the value is the node's properties plus its
// identity. The jq filter runs client-side over nodes fetched by key or streamed
// from the label, so the semantics match every other backend behind the KV port.
//
// Neo4j has no single keyspace, so a node label is the addressable container (like
// a Mongo collection or a Cassandra table): the source names the server and the
// label rides as ?label= or the handle.<label> dotted override, within the
// database named by ?database= (default "neo4j"). Credentials travel in the URL
// userinfo (Bolt basic auth) and never in a query, so the CLI's keyring support
// applies unchanged and the --verbose trace logs only the Cypher statement, never
// its parameters.
//
// A relationship type is an addressable collection too (?rel=KNOWS, or handle.:KNOWS
// with the ':' marker): a scan streams relationships of that type as {props, _type,
// _start, _end} where _start/_end are the endpoint elementIds. Relationship
// collections are read-only for now — creating an edge needs endpoint resolution
// (which nodes to connect, by which key), a further follow-up — so a write into one
// is refused rather than half-done.
package neo4j

import (
	"encoding/base64"
	"math/big"
	"strconv"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j/dbtype"

	"github.com/zsltg/iq/internal/neo4jenvelope"
	"github.com/zsltg/iq/internal/numfmt"
)

// The reserved envelope keys iq injects alongside a value's real properties live in
// internal/neo4jenvelope, shared with the offline APOC-JSON reader so both agree on
// the exact keys. _id and _labels carry a node's identity so a ?key= source still
// exposes the elementId and labels; _type/_start/_end carry a relationship's type
// and endpoints. A same-named property is overwritten by the reserved key.

// normalizeNode renders a graph node as a JSON-ready map: its properties, each
// value converted by normalizeValue, plus _id (the elementId) and _labels.
func (s *Store) normalizeNode(node neo4j.Node) map[string]any {
	out := make(map[string]any, len(node.Props))
	for k, v := range node.Props {
		out[k] = s.normalizeValue(v)
	}
	out[neo4jenvelope.FieldID] = node.ElementId
	out[neo4jenvelope.FieldLabels] = labelsToAny(node.Labels)
	return out
}

// normalizeRelationship renders a relationship as its properties plus the reserved
// _type/_start/_end envelope (the endpoint elementIds). Only the raw Cypher path
// emits relationships in v1; the collection path is node-only.
func (s *Store) normalizeRelationship(rel neo4j.Relationship) map[string]any {
	out := make(map[string]any, len(rel.Props))
	for k, v := range rel.Props {
		out[k] = s.normalizeValue(v)
	}
	out[neo4jenvelope.FieldID] = rel.ElementId
	out[neo4jenvelope.FieldType] = rel.Type
	out[neo4jenvelope.FieldStart] = rel.StartElementId
	out[neo4jenvelope.FieldEnd] = rel.EndElementId
	return out
}

// normalizeValue converts one Cypher value into a precision-aware, JSON-ready Go
// value. Integers stay exact (Cypher integers are int64, which fit an int on the
// 64-bit builds iq targets); fractional numbers follow the decimal mode; bytes
// become base64; temporal and spatial values become their canonical strings and
// objects. This is the frozen encoding contract, identical for every fetch path.
func (s *Store) normalizeValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case bool:
		return t
	case int64:
		return int(t)
	case float64:
		if s.decimal == numfmt.DecimalString {
			return strconv.FormatFloat(t, 'g', -1, 64)
		}
		return t
	case string:
		return t
	case []byte:
		return base64.StdEncoding.EncodeToString(t)
	case *big.Int:
		return t
	case time.Time:
		return t.Format(time.RFC3339Nano)
	case dbtype.Date:
		return t.String()
	case dbtype.LocalTime:
		return t.String()
	case dbtype.LocalDateTime:
		return t.String()
	case dbtype.Time:
		return t.String()
	case dbtype.Duration:
		return t.String()
	case dbtype.Point2D:
		return map[string]any{"x": t.X, "y": t.Y, "srid": int(t.SpatialRefId)}
	case dbtype.Point3D:
		return map[string]any{"x": t.X, "y": t.Y, "z": t.Z, "srid": int(t.SpatialRefId)}
	case neo4j.Node:
		return s.normalizeNode(t)
	case neo4j.Relationship:
		return s.normalizeRelationship(t)
	case dbtype.Path:
		return s.normalizePath(t)
	case []any:
		for i, e := range t {
			t[i] = s.normalizeValue(e)
		}
		return t
	case map[string]any:
		for k, e := range t {
			t[k] = s.normalizeValue(e)
		}
		return t
	default:
		return t
	}
}

// normalizePath renders a graph path as its nodes and relationships in traversal
// order, each normalized. Only the raw Cypher path can return one.
func (s *Store) normalizePath(p dbtype.Path) map[string]any {
	nodes := make([]any, len(p.Nodes))
	for i, n := range p.Nodes {
		nodes[i] = s.normalizeNode(n)
	}
	rels := make([]any, len(p.Relationships))
	for i, r := range p.Relationships {
		rels[i] = s.normalizeRelationship(r)
	}
	return map[string]any{"nodes": nodes, "relationships": rels}
}

// labelsToAny copies a node's labels into an []any so the value is homogeneous with
// the rest of the decoded JSON tree (gojq works over []any, not []string).
func labelsToAny(labels []string) []any {
	out := make([]any, len(labels))
	for i, l := range labels {
		out[i] = l
	}
	return out
}

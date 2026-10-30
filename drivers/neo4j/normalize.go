// Package neo4j adapts a Neo4j property graph to the query ports. A node label is
// modelled as Map[key, node]. The key is the value of a chosen property (?key=) or
// the elementId. The value is the node properties plus its identity.
//
// Neo4j has no single keyspace, so a node label is the addressable container (like
// a Mongo collection or a Cassandra table). The source names the server, and the
// label is ?label= or the handle.<label> dotted override, within the database named
// by ?database= (default "neo4j"). Credentials travel in the URL userinfo (Bolt
// basic auth), never in a query. The --verbose trace logs only the Cypher
// statement, never its parameters.
//
// A relationship type is an addressable collection too (?rel=KNOWS, or handle.:KNOWS
// with the ':' marker). A scan streams relationships of that type as {props, _type,
// _start, _end}, where _start and _end are the endpoint elementIds. Relationship
// collections are read-only. Creating an edge needs endpoint resolution (which
// nodes to connect, by which key), so a write into one is refused.
package neo4j

import (
	"context"
	"encoding/base64"
	"math/big"
	"strconv"
	"time"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/dbtype"

	"github.com/zsltg/iq/internal/neo4jenvelope"
	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// The reserved envelope keys live in internal/neo4jenvelope, shared with the
// offline APOC-JSON reader. iq injects them next to the real properties. The keys
// _id and _labels carry the identity of a node, so a ?key= source still exposes
// it. The keys _type, _start, and _end carry the type and endpoints of a
// relationship. A reserved key overwrites a property with the same name.

// normalizeNode renders a graph node as a JSON-ready map: its properties, each
// value converted by normalizeValue, plus _id (the elementId) and _labels.
func (s *Store) normalizeNode(node neo4j.Node, h *numfmt.Hits) map[string]any {
	out := make(map[string]any, len(node.Props))
	for k, v := range node.Props {
		h.Enter(k)
		out[k] = s.normalizeValue(v, h)
		h.Leave()
	}
	out[neo4jenvelope.FieldID] = node.ElementId
	out[neo4jenvelope.FieldLabels] = labelsToAny(node.Labels)
	return out
}

// normalizeRelationship renders a relationship as its properties plus the reserved
// _type/_start/_end envelope (the endpoint elementIds). Only the raw Cypher path
// emits relationships in v1. The collection path is node-only.
func (s *Store) normalizeRelationship(rel neo4j.Relationship, h *numfmt.Hits) map[string]any {
	out := make(map[string]any, len(rel.Props))
	for k, v := range rel.Props {
		h.Enter(k)
		out[k] = s.normalizeValue(v, h)
		h.Leave()
	}
	out[neo4jenvelope.FieldID] = rel.ElementId
	out[neo4jenvelope.FieldType] = rel.Type
	out[neo4jenvelope.FieldStart] = rel.StartElementId
	out[neo4jenvelope.FieldEnd] = rel.EndElementId
	return out
}

// normalizeValue converts one Cypher value into a precision-aware, JSON-ready Go
// value. Integers stay exact (Cypher integers are int64, which fit an int on the
// 64-bit builds iq targets). Fractional numbers follow the decimal mode. Bytes
// become base64. Temporal and spatial values become their canonical strings and
// objects. A NaN or an infinity has no JSON form, so it becomes a string (see
// numfmt.FiniteFloat) in every decimal mode, and h records its path. h can be
// nil. This is the frozen encoding contract, identical for every fetch path.
func (s *Store) normalizeValue(v any, h *numfmt.Hits) any {
	switch t := v.(type) {
	case nil:
		return nil
	case bool:
		return t
	case int64:
		return int(t)
	case float64:
		return s.floatValue(t, h)
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
		return s.normalizeNode(t, h)
	case neo4j.Relationship:
		return s.normalizeRelationship(t, h)
	case dbtype.Path:
		return s.normalizePath(t, h)
	case []any:
		for i, e := range t {
			h.EnterIndex(i)
			t[i] = s.normalizeValue(e, h)
			h.Leave()
		}
		return t
	case map[string]any:
		for k, e := range t {
			h.Enter(k)
			t[k] = s.normalizeValue(e, h)
			h.Leave()
		}
		return t
	default:
		return t
	}
}

// floatValue renders a Cypher float. A non-finite float becomes its text and is
// recorded in h. A finite float follows the decimal mode.
func (s *Store) floatValue(f float64, h *numfmt.Hits) any {
	v := h.Float(f)
	if text, converted := v.(string); converted {
		return text
	}
	if s.decimal == numfmt.DecimalString {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return f
}

// normalizePath renders a graph path as its nodes and relationships in traversal
// order, each normalized. Only the raw Cypher path can return one.
func (s *Store) normalizePath(p dbtype.Path, h *numfmt.Hits) map[string]any {
	nodes := make([]any, len(p.Nodes))
	h.Enter("nodes")
	for i, n := range p.Nodes {
		h.EnterIndex(i)
		nodes[i] = s.normalizeNode(n, h)
		h.Leave()
	}
	h.Leave()
	rels := make([]any, len(p.Relationships))
	h.Enter("relationships")
	for i, r := range p.Relationships {
		h.EnterIndex(i)
		rels[i] = s.normalizeRelationship(r, h)
		h.Leave()
	}
	h.Leave()
	return map[string]any{"nodes": nodes, "relationships": rels}
}

// labelsToAny copies a node's labels into an []any. The value is then homogeneous
// with the rest of the decoded JSON tree (gojq works over []any, not []string).
func labelsToAny(labels []string) []any {
	out := make([]any, len(labels))
	for i, l := range labels {
		out[i] = l
	}
	return out
}

// normalizeEntity normalizes the value of one key. It reports each NaN or
// infinity that it converted to the notice sink of ctx.
func (s *Store) normalizeEntity(ctx context.Context, key string, v any) any {
	h := &numfmt.Hits{}
	out := s.normalizeValue(v, h)
	query.NoteNonFinite(ctx, key, h.Fields...)
	return out
}

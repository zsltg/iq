// Package couchdb adapts an Apache CouchDB database to the query ports. A database
// is modelled as Map[key, document]: the _id of a document is the key, and the
// document is the value.
//
// A CouchDB server hosts many top-level databases and nothing between the server
// and its documents. So the database is the addressable keyspace (like a Mongo
// collection or a Cassandra table). The source names the server, and the database
// is ?database= or the handle.<db> dotted override. Credentials travel in the URL
// userinfo (HTTP basic auth), never in a request path.
package couchdb

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/zsltg/iq/internal/numfmt"
)

// decodeDoc parses a CouchDB document body into JSON-ready Go values. It decodes
// numbers with UseNumber so precision survives: an integer becomes an exact int
// or *big.Int, and a fractional number follows the decimal mode. Plain
// json.Unmarshal collapses every number to a float64 and silently loses precision
// beyond 2^53. _id and _rev are kept in the value. They are legitimate CouchDB
// document fields, so a round-trip stays honest.
func decodeDoc(raw json.RawMessage, mode numfmt.DecimalMode) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode couchdb document: %w", err)
	}
	return normalizeMap(doc, mode), nil
}

// normalizeMap converts every value of a document to its precision-aware Go form,
// preserving keys.
func normalizeMap(m map[string]any, mode numfmt.DecimalMode) map[string]any {
	for k, v := range m {
		m[k] = numfmt.ConvertNumbers(v, mode)
	}
	return m
}

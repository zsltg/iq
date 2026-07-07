// Package couchdb adapts an Apache CouchDB database to the query ports. A database
// is modelled as Map[key, document]: a document's _id is the key, the document is
// the value. The jq filter runs client-side over documents fetched by _id or
// streamed from the database, so the semantics match every other backend behind
// the KV port.
//
// A CouchDB server hosts many top-level databases and nothing between the server
// and its documents, so the database is the addressable keyspace (like a Mongo
// collection or a Cassandra table): the source names the server and the database
// rides as ?database= or the handle.<db> dotted override. Credentials travel in
// the URL userinfo (HTTP basic auth) and never in a request path, so the CLI's
// keyring support applies unchanged and the --verbose trace logs only method+path.
package couchdb

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/zsltg/iq/internal/numfmt"
)

// decodeDoc parses a CouchDB document body into JSON-ready Go values, decoding
// numbers deliberately (UseNumber) so precision survives: an integer becomes an
// exact int or *big.Int, and a fractional number follows the decimal mode. Plain
// json.Unmarshal would collapse every number to a float64 and silently lose
// precision beyond 2^53. _id and _rev are kept in the value — they are legitimate
// CouchDB document fields, so a round-trip stays honest.
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

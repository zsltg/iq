// Package elasticsearch adapts an Elasticsearch index to the query ports. An index
// is modelled as Map[key, document]: a document's _id is the key, its _source is the
// value. The jq filter runs client-side over documents fetched by _id or streamed
// from the index, so the semantics match every other backend behind the KV port.
//
// An Elasticsearch server hosts many top-level indices and nothing between the
// server and its documents, so the index is the addressable keyspace (like a Mongo
// collection or a CouchDB database): the source names the server and the index rides
// as ?index= or the handle.<index> dotted override. Credentials travel in the URL
// userinfo (HTTP basic auth) and never in a request path, so the CLI's keyring
// support applies unchanged and the --verbose trace logs only method+path.
package elasticsearch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/zsltg/iq/internal/numfmt"
)

// decodeSource parses a hit's _source into JSON-ready Go values and injects the
// hit's _id, so a plain .[] stream is self-describing (the document carries its own
// key, exactly like a Mongo document's _id). Numbers are decoded deliberately
// (UseNumber) so precision survives: an integer becomes an exact int or *big.Int,
// and a fractional number follows the decimal mode. A hit with no _source (a
// stored-fields-only response) still yields {"_id": id}. Elasticsearch reserves _id
// as a metadata field, so it is never a real _source field and the injection never
// collides.
func decodeSource(raw json.RawMessage, id string, mode numfmt.DecimalMode) (map[string]any, error) {
	doc := map[string]any{}
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode elasticsearch _source: %w", err)
		}
		doc = normalizeMap(doc, mode)
	}
	doc["_id"] = id
	return doc, nil
}

// normalizeMap converts every value of a document to its precision-aware Go form,
// preserving keys.
func normalizeMap(m map[string]any, mode numfmt.DecimalMode) map[string]any {
	for k, v := range m {
		m[k] = convertNumbers(v, mode)
	}
	return m
}

// convertNumbers walks a decoded value, converting every json.Number to its
// precision-aware Go form and leaving other values untouched, recursing into
// objects and arrays.
func convertNumbers(v any, mode numfmt.DecimalMode) any {
	switch t := v.(type) {
	case json.Number:
		return convertNumber(t, mode)
	case map[string]any:
		for k, e := range t {
			t[k] = convertNumbers(e, mode)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = convertNumbers(e, mode)
		}
		return t
	default:
		return t
	}
}

// convertNumber resolves one JSON number token. An integer (no '.', 'e', or 'E') is
// always exact: an int when it fits, else a *big.Int — gojq does exact arithmetic on
// both, so integers are never lossy regardless of the mode. A fractional number is a
// float64 in auto and number mode, or its exact literal string in string mode.
func convertNumber(n json.Number, mode numfmt.DecimalMode) any {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		if i, err := n.Int64(); err == nil && int64(int(i)) == i {
			return int(i)
		}
		if bi, ok := new(big.Int).SetString(s, 10); ok {
			return bi
		}
		return s
	}
	if mode == numfmt.DecimalString {
		return s
	}
	f, _ := n.Float64()
	return f
}

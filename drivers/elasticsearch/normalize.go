// Package elasticsearch adapts an Elasticsearch index to the query ports. An
// index is a Map[key, document]: the _id is the key and the _source is the value.
// The jq filter runs client-side over documents that are fetched by _id or
// streamed from the index.
//
// A server hosts many top-level indices and nothing between the server and its
// documents. So the index is the addressable keyspace (like a Mongo collection or
// a CouchDB database). The source names the server. The index is ?index= or the
// handle.<index> dotted override. Credentials travel in the URI userinfo (HTTP
// basic auth) and never in a request path. The --verbose trace logs method and
// path only.
package elasticsearch

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/zsltg/iq/internal/numfmt"
)

// injectedIDField is the metadata field that decodeSource injects into every decoded
// document. It carries the hit's _id. Elasticsearch reserves _id as a metadata field,
// so the raw _source bytes that the prefilter sees never contain it. A predicate
// rooted at this field cannot be decided on raw bytes (see referencesInjectedID).
// documentBody strips it on write.
const injectedIDField = "_id"

// decodeSource parses a hit's _source into JSON-ready Go values and injects the
// hit's _id, so the document carries its own key (like a Mongo _id). It decodes with
// UseNumber to keep precision: an integer becomes an exact int or *big.Int, and a
// fractional number follows the decimal mode. A hit with no _source (a
// stored-fields-only response) or a null _source yields {"_id": id}. The injection
// never collides, because _id is never a real _source field.
func decodeSource(raw json.RawMessage, id string, mode numfmt.DecimalMode) (map[string]any, error) {
	doc := map[string]any{}
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode elasticsearch _source: %w", err)
		}
		if doc == nil {
			// A JSON null leaves the map nil, and a write into it panics.
			doc = map[string]any{}
		}
		doc = normalizeMap(doc, mode)
	}
	doc[injectedIDField] = id
	return doc, nil
}

// normalizeMap converts every value of a document to its precision-aware Go form,
// preserving keys.
func normalizeMap(m map[string]any, mode numfmt.DecimalMode) map[string]any {
	for k, v := range m {
		m[k] = numfmt.ConvertNumbers(v, mode)
	}
	return m
}

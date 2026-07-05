// Package mongo adapts a MongoDB collection to the query ports. A collection is
// modelled as Map[key, document]: a document's _id (rendered as a string) is the
// key, the document is the value. The jq filter runs client-side over documents
// fetched by _id or streamed from the collection.
package mongo

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
)

// normalize converts a decoded BSON value into a JSON-ready Go value the jq
// engine accepts: nil, bool, int, float64, string, []any, map[string]any. BSON's
// richer types are rendered to their natural JSON form — an ObjectID to its hex
// string, a date to RFC 3339 — so filters read naturally and the encoding is a
// frozen contract, the MongoDB analogue of the Redis type normalization. A
// Decimal128 follows the store's decimal mode: exact string, or a bare number.
func (s *Store) normalize(v any) any {
	switch t := v.(type) {
	case nil, bool, string, float64:
		return t
	case int:
		return t
	case int32:
		return int(t)
	case int64:
		return int(t)
	case bson.ObjectID:
		return t.Hex()
	case bson.DateTime:
		return t.Time().UTC().Format(time.RFC3339Nano)
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case bson.Decimal128:
		return decimalValue(t, s.decimal)
	case bson.Binary:
		return base64.StdEncoding.EncodeToString(t.Data)
	case bson.M:
		return s.normalizeMap(t)
	case map[string]any:
		return s.normalizeMap(t)
	case bson.D:
		out := make(map[string]any, len(t))
		for _, e := range t {
			out[e.Key] = s.normalize(e.Value)
		}
		return out
	case bson.A:
		return s.normalizeSlice(t)
	case []any:
		return s.normalizeSlice(t)
	default:
		// A BSON type without a first-class JSON form (timestamp, regex, …) is
		// rendered as its Go string form rather than dropped, so nothing is lost
		// silently. Extend with a dedicated case when one needs a stable shape.
		return fmt.Sprintf("%v", t)
	}
}

// decimalValue renders a BSON Decimal128 per the decimal mode. In auto and string
// mode it keeps the exact decimal literal as a string; in number mode it parses
// to a float64 (lossy beyond float64), except NaN and ±Infinity, which have no
// JSON-number form and so stay strings rather than degrade to null.
func decimalValue(d bson.Decimal128, mode numfmt.DecimalMode) any {
	str := d.String()
	if mode != numfmt.DecimalNumber {
		return str
	}
	switch str {
	case "NaN", "Infinity", "-Infinity":
		return str
	}
	if f, err := strconv.ParseFloat(str, 64); err == nil {
		return f
	}
	return str
}

// normalizeMap normalizes every value of a document, preserving keys.
func (s *Store) normalizeMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = s.normalize(v)
	}
	return out
}

// normalizeSlice normalizes every element of an array, preserving order.
func (s *Store) normalizeSlice(a []any) []any {
	out := make([]any, len(a))
	for i, v := range a {
		out[i] = s.normalize(v)
	}
	return out
}

// keyOf renders a document's _id to the string key the collection is keyed by,
// the inverse of how idValues turns a key back into candidate _id matches. An
// ObjectID becomes its hex string; a string _id is itself; other scalars use
// their natural form.
func keyOf(id any) string {
	switch t := id.(type) {
	case bson.ObjectID:
		return t.Hex()
	case string:
		return t
	case int32:
		return fmt.Sprintf("%d", t)
	case int64:
		return fmt.Sprintf("%d", t)
	case int:
		return fmt.Sprintf("%d", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// idValues turns a requested string key into the candidate _id values to match:
// the string itself and, when the key is a 24-character hex string, the ObjectID
// it encodes. This matches both string-keyed and ObjectID-keyed collections
// without the caller having to know which a collection uses.
func idValues(keys []string) bson.A {
	out := make(bson.A, 0, len(keys))
	for _, k := range keys {
		out = append(out, k)
		if oid, err := bson.ObjectIDFromHex(k); err == nil {
			out = append(out, oid)
		}
	}
	return out
}

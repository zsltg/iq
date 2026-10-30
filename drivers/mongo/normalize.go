// Package mongo adapts a MongoDB collection to the query ports. A collection is
// modelled as Map[key, document]: a document's _id (rendered as a string) is the
// key, the document is the value. The jq filter runs client-side over documents
// fetched by _id or streamed from the collection.
package mongo

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// Normalize converts a decoded BSON value into a JSON-ready Go value the jq
// engine accepts: nil, bool, int, float64, string, []any, map[string]any. Richer
// BSON types get their natural JSON form: an ObjectID becomes its hex string, and
// a date RFC 3339. The encoding is a frozen contract. A Decimal128 follows dec:
// exact string, or a bare number. A NaN or an infinity has no JSON form, so it
// becomes a string (see numfmt.FiniteFloat), and h records its path. h can be
// nil. Normalize is exported so a dump reader (the file backend) produces the
// same shape as a live collection scan.
func Normalize(v any, dec numfmt.DecimalMode, h *numfmt.Hits) any {
	switch t := v.(type) {
	case nil, bool, string:
		return t
	case float64:
		return h.Float(t)
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
		return decimalValue(t, dec)
	case bson.Binary:
		return base64.StdEncoding.EncodeToString(t.Data)
	case bson.M:
		return normalizeMap(t, dec, h)
	case map[string]any:
		return normalizeMap(t, dec, h)
	case bson.D:
		return normalizeDoc(t, dec, h)
	case bson.A:
		return normalizeSlice(t, dec, h)
	case []any:
		return normalizeSlice(t, dec, h)
	default:
		// A BSON type without a first-class JSON form (timestamp, regex, and so
		// on) is rendered as its Go string form and not dropped. So nothing is
		// lost silently. Extend with a dedicated case when one needs a stable
		// shape.
		return fmt.Sprintf("%v", t)
	}
}

// normalizeDoc converts an ordered BSON document to a map. Each value goes
// through Normalize, and its key goes into the path of h.
func normalizeDoc(d bson.D, dec numfmt.DecimalMode, h *numfmt.Hits) map[string]any {
	out := make(map[string]any, len(d))
	for _, e := range d {
		h.Enter(e.Key)
		out[e.Key] = Normalize(e.Value, dec, h)
		h.Leave()
	}
	return out
}

// Document normalizes one decoded document and returns its key and value. It
// reports each NaN or infinity that it converted to the notice sink of ctx. It
// is exported so a dump reader keys and converts a document as a live scan does.
func Document(ctx context.Context, doc map[string]any, dec numfmt.DecimalMode) (string, any) {
	key := KeyOf(doc["_id"])
	h := &numfmt.Hits{}
	val := Normalize(doc, dec, h)
	query.NoteNonFinite(ctx, key, h.Fields...)
	return key, val
}

// normalizeReply normalizes the reply of a raw command. A reply has no key, so
// a conversion is reported under the name "reply".
func (s *Store) normalizeReply(ctx context.Context, res map[string]any) any {
	h := &numfmt.Hits{}
	val := Normalize(res, s.decimal, h)
	query.NoteNonFinite(ctx, "reply", h.Fields...)
	return val
}

// decimalValue renders a BSON Decimal128 per the decimal mode. In auto and string
// mode it keeps the exact decimal literal as a string. In number mode it parses
// to a float64 (lossy beyond float64). NaN and ±Infinity have no JSON-number form,
// so they stay strings.
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
func normalizeMap(m map[string]any, dec numfmt.DecimalMode, h *numfmt.Hits) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		h.Enter(k)
		out[k] = Normalize(v, dec, h)
		h.Leave()
	}
	return out
}

// normalizeSlice normalizes every element of an array, preserving order.
func normalizeSlice(a []any, dec numfmt.DecimalMode, h *numfmt.Hits) []any {
	out := make([]any, len(a))
	for i, v := range a {
		h.EnterIndex(i)
		out[i] = Normalize(v, dec, h)
		h.Leave()
	}
	return out
}

// KeyOf renders the _id of a document to the string key that the collection is
// keyed by. It is the inverse of how idValues turns a key back into candidate
// _id matches. An ObjectID becomes its hex string. A string _id is itself.
// Other scalars use their natural form. It is exported so a dump reader keys
// mongodump/mongoexport documents exactly as a live collection scan does.
func KeyOf(id any) string {
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

// idValues turns a requested string key into the candidate _id values to match.
// These are the string itself and, when the key is a 24-character hex string,
// the ObjectID that it encodes. This matches both string-keyed and
// ObjectID-keyed collections without the caller having to know which a
// collection uses.
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

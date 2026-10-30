package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/zsltg/iq/internal/neo4jenvelope"
	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// neo4jSource streams an APOC JSON export (apoc.export.json.*): JSON Lines (the
// default) or one JSON array of {"type":"node",…}/{"type":"relationship",…} objects.
// A dump holds the whole graph, but iq addresses one keyspace at a time. So the
// reader emits one kind: the nodes of ?label=<Label>, or the relationships of
// ?rel=<Type>. It keys them like a live scan and wraps them in the same
// _id/_labels/_type/_start/_end envelope (see drivers/neo4j). A keyspace selector is
// required, and the reader does not guess one.
func neo4jSource(r io.Reader, size int, dec numfmt.DecimalMode, hints Hints) (query.RecordSource, error) {
	return neo4jSourceCap(capped{r, query.MaxRecordBytes}, size, dec, hints)
}

// neo4jSourceCap is neo4jSource with a limit on the size of one entry.
func neo4jSourceCap(src capped, size int, dec numfmt.DecimalMode, hints Hints) (query.RecordSource, error) {
	mode, name, err := neo4jKeyspace(hints)
	if err != nil {
		return nil, err
	}
	keyProp := hints.Key
	return func(ctx context.Context, fn func(batch []query.Record) error) error {
		st, ok, err := openJSONStream(src, "neo4j json", "read neo4j json array")
		if err != nil || !ok {
			return err
		}
		st.jd.UseNumber() // keep integers exact and distinct from floats.
		pg := newRecordPager(size, fn)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			var e apocEntry
			if more, err := st.next(&e); err != nil || !more {
				if err != nil {
					return err
				}
				break
			}
			rec, keep, err := recordForEntry(e, mode, name, keyProp, dec)
			if err != nil {
				return err
			}
			if !keep {
				continue
			}
			if err := pg.add(rec); err != nil {
				return err
			}
		}
		return pg.flush()
	}, nil
}

// neo4jKind is the one keyspace an APOC dump read exposes: a node label or a
// relationship type.
type neo4jKind int

const (
	nodeKind neo4jKind = iota
	relKind
)

// neo4jKeyspace resolves the ?label=/?rel= hints to the single kind and name that
// the read exposes. Exactly one selector is required. Both is a contradiction,
// and neither leaves the keyspace unnamed. So either case is a clear, up-front
// error, not a silent full-graph dump.
func neo4jKeyspace(hints Hints) (neo4jKind, string, error) {
	switch {
	case hints.Label != "" && hints.Rel != "":
		return 0, "", errors.New("neo4j dump: set only one of ?label= or ?rel=, not both")
	case hints.Label != "":
		return nodeKind, hints.Label, nil
	case hints.Rel != "":
		return relKind, hints.Rel, nil
	default:
		return 0, "", errors.New("neo4j dump needs a keyspace: add ?label=<Label> or ?rel=<Type> to the file url")
	}
}

// apocEntry is one line of an APOC JSON export: a node ("type":"node", labels,
// properties) or a relationship ("type":"relationship", label, start/end endpoints,
// properties). Numbers arrive as json.Number because the decoder runs UseNumber.
type apocEntry struct {
	Type       string         `json:"type"`
	ID         string         `json:"id"`
	Labels     []string       `json:"labels"`
	Label      string         `json:"label"`
	Properties map[string]any `json:"properties"`
	Start      apocEndpoint   `json:"start"`
	End        apocEndpoint   `json:"end"`
}

// apocEndpoint is a relationship endpoint reference in an APOC export. iq keeps
// only its id for the _start/_end envelope.
type apocEndpoint struct {
	ID string `json:"id"`
}

// recordForEntry turns one export entry into the record that a live scan of the
// selected keyspace yields. It reports keep=false for an entry of the other kind
// or a non-matching label/type (a dump interleaves every label and type). An
// unrecognized "type" is a hard error, so a wrong ?format= or a corrupt line
// fails fast.
func recordForEntry(e apocEntry, mode neo4jKind, name, keyProp string, dec numfmt.DecimalMode) (query.Record, bool, error) {
	switch e.Type {
	case "node":
		if mode != nodeKind || !contains(e.Labels, name) {
			return query.Record{}, false, nil
		}
		return nodeRecord(e, keyProp, dec), true, nil
	case "relationship":
		if mode != relKind || e.Label != name {
			return query.Record{}, false, nil
		}
		return relRecord(e, keyProp, dec), true, nil
	default:
		return query.Record{}, false, fmt.Errorf("neo4j dump object has type %q; expected \"node\" or \"relationship\"", e.Type)
	}
}

// nodeRecord renders one node entry as the node envelope of the live driver. The
// envelope holds its normalized properties plus _id (the export id) and _labels,
// keyed by the export id or the ?key= property.
func nodeRecord(e apocEntry, keyProp string, dec numfmt.DecimalMode) query.Record {
	val := normalizeProps(e.Properties, dec)
	key := recordKey(e.ID, e.Properties, keyProp)
	val[neo4jenvelope.FieldID] = e.ID
	val[neo4jenvelope.FieldLabels] = labelsToAny(e.Labels)
	return query.Record{Key: key, Type: "node", Value: val}
}

// relRecord renders one relationship entry as the relationship envelope of the
// live driver. The envelope holds its normalized properties plus _id, _type, and
// the _start/_end endpoint ids, keyed by the export id or the ?key= property.
func relRecord(e apocEntry, keyProp string, dec numfmt.DecimalMode) query.Record {
	val := normalizeProps(e.Properties, dec)
	key := recordKey(e.ID, e.Properties, keyProp)
	val[neo4jenvelope.FieldID] = e.ID
	val[neo4jenvelope.FieldType] = e.Label
	val[neo4jenvelope.FieldStart] = e.Start.ID
	val[neo4jenvelope.FieldEnd] = e.End.ID
	return query.Record{Key: key, Type: "relationship", Value: val}
}

// recordKey picks the map key of a record. By default it is the export id (globally
// unique within a kind). If ?key= is set, it is the string form of that property. If
// that property is absent or null, it falls back to the export id, so the scan stays
// total.
func recordKey(id string, props map[string]any, keyProp string) string {
	if keyProp == "" {
		return id
	}
	v, ok := props[keyProp]
	if !ok || v == nil {
		return id
	}
	return keyString(v)
}

// keyString renders a property value as a map key the way Cypher's toString
// does. It renders a string as-is, a number by its literal, and a bool as
// true/false.
func keyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(t)
	}
}

// normalizeProps copies a property map with each value normalized. A node's
// stored properties then survive as a fresh map that the envelope keys can be
// added to.
func normalizeProps(props map[string]any, dec numfmt.DecimalMode) map[string]any {
	out := make(map[string]any, len(props))
	for k, v := range props {
		out[k] = normalizeJSONValue(v, dec)
	}
	return out
}

// normalizeJSONValue narrows a decoded APOC value to the precision-aware form of iq,
// as the live driver does. An integral number becomes an exact int (a *big.Int past
// int64). A fractional one becomes a float64, or its exact literal under the string
// decimal mode. Strings, bools, and nested containers pass through with their numbers
// narrowed. The APOC string encodings of temporal and spatial values are already
// JSON and pass through verbatim.
func normalizeJSONValue(v any, dec numfmt.DecimalMode) any {
	switch t := v.(type) {
	case json.Number:
		return normalizeNumber(t, dec)
	case map[string]any:
		for k, e := range t {
			t[k] = normalizeJSONValue(e, dec)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = normalizeJSONValue(e, dec)
		}
		return t
	default:
		return t
	}
}

// normalizeNumber converts a json.Number to the same Go value that a live Cypher
// fetch yields. An integral literal becomes an int (a *big.Int when it overflows
// int64). A fractional one becomes a float64. Under DecimalString it becomes its
// exact literal string.
func normalizeNumber(n json.Number, dec numfmt.DecimalMode) any {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
		if bi, ok := new(big.Int).SetString(s, 10); ok {
			return bi
		}
	}
	f, err := n.Float64()
	if err != nil {
		return s // unparseable as a float; keep the literal rather than lose it.
	}
	if dec == numfmt.DecimalString {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return f
}

// labelsToAny copies a node's labels into an []any. The value is then
// homogeneous with the rest of the decoded JSON tree (gojq works over []any,
// not []string). It matches the node envelope of the live driver.
func labelsToAny(labels []string) []any {
	out := make([]any, len(labels))
	for i, l := range labels {
		out[i] = l
	}
	return out
}

// contains reports whether name is one of labels.
func contains(labels []string, name string) bool {
	return slices.Contains(labels, name)
}

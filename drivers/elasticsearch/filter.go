package elasticsearch

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/zsltg/iq/internal/predicate"
)

// fieldClass is the literal kind a mapped field can match exactly with a term query.
type fieldClass int

const (
	stringClass fieldClass = iota // keyword, ip, or a text field's keyword sub-field
	numberClass                   // an integer or floating mapping type
	boolClass                     // boolean
)

// exactField is a top-level field an equality can push as a term query: the concrete
// term path (the field, or its .keyword sub-field for a text field) and the literal
// class the field accepts.
type exactField struct {
	path  string
	class fieldClass
}

// mappingProperty is the subset of an index-mapping property the driver reads: its
// type, any multi-fields (a text field's keyword sub-field), and whether it is an
// object (has nested properties, so not a scalar term target).
type mappingProperty struct {
	Type       string                     `json:"type"`
	Fields     map[string]mappingProperty `json:"fields"`
	Properties map[string]json.RawMessage `json:"properties"`
}

// exact reports whether a top-level field named name can back an exact term match,
// and how. A keyword/numeric/boolean/ip field matches on itself; a text field
// matches on its keyword sub-field when it has one; everything else (object, nested,
// date, geo, or a text field with no keyword sub-field) cannot be pushed safely.
func (p mappingProperty) exact(name string) (exactField, bool) {
	if len(p.Properties) > 0 {
		return exactField{}, false // an object/nested field is not a scalar term target.
	}
	switch p.Type {
	case "keyword", "ip":
		return exactField{path: name, class: stringClass}, true
	case "boolean":
		return exactField{path: name, class: boolClass}, true
	case "long", "integer", "short", "byte",
		"double", "float", "half_float", "scaled_float", "unsigned_long":
		return exactField{path: name, class: numberClass}, true
	case "text":
		for sub, fp := range p.Fields {
			if fp.Type == "keyword" {
				return exactField{path: name + "." + sub, class: stringClass}, true
			}
		}
	}
	return exactField{}, false
}

// indexProperties is the "mappings" section of one index's mapping reply.
type indexProperties struct {
	Properties map[string]mappingProperty `json:"properties"`
}

// indexMapping is one index's entry in a GET _mapping reply.
type indexMapping struct {
	Mappings indexProperties `json:"mappings"`
}

// readExactFields reads the index mapping once and returns the top-level fields whose
// type makes an equality pushable as a term query.
func (s *Store) readExactFields(ctx context.Context) (map[string]exactField, error) {
	var raw map[string]indexMapping
	if err := s.request(ctx, "get mapping", http.MethodGet, "/"+s.index+"/_mapping", nil, &raw); err != nil {
		return nil, err
	}
	return exactFieldsFrom(raw), nil
}

// exactFieldsFrom reduces a GET _mapping reply to the top-level fields an equality can
// push as a term query. When the index resolves to more than one mapping (an alias),
// a field is kept only when every mapping agrees it is exact and of the same class, so
// a term is never pushed onto a field that is analyzed text — or a different exact
// type — somewhere in the fan-out.
func exactFieldsFrom(raw map[string]indexMapping) map[string]exactField {
	seen := map[string]exactField{}
	conflict := map[string]bool{}
	for _, idx := range raw {
		for name, prop := range idx.Mappings.Properties {
			ef, ok := prop.exact(name)
			if !ok {
				conflict[name] = true
				continue
			}
			if prev, dup := seen[name]; dup && prev != ef {
				conflict[name] = true
				continue
			}
			seen[name] = ef
		}
	}
	out := make(map[string]exactField, len(seen))
	for name, ef := range seen {
		if !conflict[name] {
			out[name] = ef
		}
	}
	return out
}

// ScanFiltered streams only the documents a bool query pre-selects for pred, letting
// the server narrow the scan. pred is a conservative superset (the caller re-runs the
// full jq per page), so returning extra documents is safe and returning too few is
// not. When pred compiles to nothing that narrows — a range, regex, length, negation,
// or an equality on a field the mapping does not make exactly matchable — the scan
// falls back to a plain point-in-time walk, which is correct and no costlier than a
// match-everything query.
//
// Whatever the server query leaves behind, a client-side raw-byte prefilter runs the
// full predicate over each hit's raw _source before it is decoded and drops any hit
// rawpred proves the predicate rejects — so a full-scan fallback or a partially-pushed
// scan skips the (dominant) decode of the documents the server could not exclude. The
// prefilter is bypassed in two cases where it would be wrong or wasted: when the server
// query already captures the predicate exactly (the raw scan would only re-confirm
// matches), and when the predicate references the injected _id field the raw _source
// does not carry (rawpred would read a clean absence and could wrongly drop a document
// whose id matches).
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	if s.index == "" {
		return errNoIndex
	}
	// toQuery yields a nil query for a predicate it cannot narrow, and pagedSearch
	// reads a nil query as a full point-in-time walk, so the narrowing flag needs no
	// separate branch here: a non-narrowing predicate simply pages the whole index and
	// the prefilter (when enabled) trims it.
	query, _ := s.toQuery(pred)
	prefilter := pred
	if s.exactPush(pred) || referencesInjectedID(pred) {
		prefilter = nil
	}
	return s.pagedSearch(ctx, query, prefilter, fn)
}

// exactPush reports whether toQuery's translation of n captures it exactly — the
// backend returns precisely jq's matching set, so a client-side prefilter over the
// same documents would only re-confirm matches and is pure overhead. Only an equality
// pushed as a term query is treated as exact: it matches the field value and nothing
// else. Existence, non-existence, ranges, negations, and any conjunct the translation
// had to drop leave the backend over- or under-inclusive, so those keep the prefilter;
// an And or Or is exact only when every child is (an And with a dropped conjunct, or an
// Or with any non-exact branch, is not).
func (s *Store) exactPush(n predicate.Node) bool {
	switch t := n.(type) {
	case predicate.Eq:
		_, ok := s.eqQuery(t)
		return ok
	case predicate.And:
		return allExact(s, t)
	case predicate.Or:
		return allExact(s, t)
	default:
		return false
	}
}

// allExact reports whether every child of a non-empty composite is exactly pushable.
// An empty composite is never exact: it would claim exactness with no term backing it.
func allExact(s *Store, children []predicate.Node) bool {
	if len(children) == 0 {
		return false
	}
	for _, c := range children {
		if !s.exactPush(c) {
			return false
		}
	}
	return true
}

// referencesInjectedID reports whether any field path in the predicate tree is rooted
// at the injected _id field. That field is decodeSource's injection, not a real
// _source field, so rawpred — which sees only the raw _source — would read it as a
// clean absence and could wrongly reject a document whose injected id satisfies the
// predicate. When it does, ScanFiltered disables the prefilter for the whole scan so
// every document is delivered for the full jq to filter. It walks every node that
// carries a path, including the element conditions of ElemMatch/NoneMatch, so no
// _id-rooted path anywhere in the tree is missed.
func referencesInjectedID(n predicate.Node) bool {
	switch t := n.(type) {
	case predicate.Eq:
		return rootedAtID(t.Path)
	case predicate.Ne:
		return rootedAtID(t.Path)
	case predicate.Cmp:
		return rootedAtID(t.Path)
	case predicate.Regex:
		return rootedAtID(t.Path)
	case predicate.Size:
		return rootedAtID(t.Path)
	case predicate.Exists:
		return rootedAtID(t.Path)
	case predicate.NotExists:
		return rootedAtID(t.Path)
	case predicate.ElemMatch:
		return rootedAtID(t.Path) || referencesInjectedID(t.Cond)
	case predicate.NoneMatch:
		return rootedAtID(t.Path) || referencesInjectedID(t.Cond)
	case predicate.And:
		return anyReferencesInjectedID(t)
	case predicate.Or:
		return anyReferencesInjectedID(t)
	default:
		return false
	}
}

// anyReferencesInjectedID reports whether any child of a composite references the
// injected _id field.
func anyReferencesInjectedID(children []predicate.Node) bool {
	for _, c := range children {
		if referencesInjectedID(c) {
			return true
		}
	}
	return false
}

// rootedAtID reports whether a field path's first segment is the injected _id field.
func rootedAtID(path []string) bool {
	return len(path) > 0 && path[0] == injectedIDField
}

// toQuery translates a neutral predicate into an Elasticsearch query. The second
// return is whether the query actually narrows the scan: false means the node (or an
// unsafe part of it) could not be pushed without risking wrongly excluding a jq
// match, so the caller must fall back to a full scan. Only pure-superset,
// over-inclusion-safe nodes are pushed — equality on an exactly-matchable field and
// existence — mirroring the Neo4j adapter's conservative scope. Ranges are not
// pushed because an Elasticsearch range excludes a missing field and orders types
// differently from jq's cross-type total order; the negations and polymorphic
// length/regex/any tests are likewise left to the client.
func (s *Store) toQuery(n predicate.Node) (map[string]any, bool) {
	switch t := n.(type) {
	case predicate.Eq:
		return s.eqQuery(t)
	case predicate.Exists:
		return existsQuery(field(t.Path)), true
	case predicate.NotExists:
		// Exact: jq's `has | not` is precisely a missing field, which
		// must_not(exists) matches exactly.
		return map[string]any{"bool": map[string]any{"must_not": existsQuery(field(t.Path))}}, true
	case predicate.And:
		return s.andQuery(t)
	case predicate.Or:
		return s.orQuery(t)
	default:
		// Cmp, Regex, Size, Ne, NoneMatch, ElemMatch, and any unknown node: do not narrow.
		return nil, false
	}
}

// eqQuery builds a term query for an equality, but only when the field is a single
// top-level scalar the mapping makes exactly matchable and the literal's type matches
// the field's class. A null literal is never pushed: jq reads a missing field as null
// so `== null` matches absent-or-null, which no single term query reproduces safely.
func (s *Store) eqQuery(e predicate.Eq) (map[string]any, bool) {
	if e.Value == nil || len(e.Path) != 1 {
		return nil, false
	}
	ef, ok := s.exact[e.Path[0]]
	if !ok || !matchesLiteral(ef.class, e.Value) {
		return nil, false
	}
	return map[string]any{"term": map[string]any{ef.path: e.Value}}, true
}

// matchesLiteral reports whether a pushdown literal's Go type matches the field's
// class, so a term is only pushed when Elasticsearch will not reject or coerce it. A
// mismatch (e.g. a string literal against a numeric field) is left client-side, where
// jq also treats the cross-type comparison as unequal.
func matchesLiteral(class fieldClass, v any) bool {
	switch class {
	case stringClass:
		_, ok := v.(string)
		return ok
	case numberClass:
		_, ok := v.(float64)
		return ok
	case boolClass:
		_, ok := v.(bool)
		return ok
	default:
		return false
	}
}

// existsQuery builds an exists query for a field path.
func existsQuery(path string) map[string]any {
	return map[string]any{"exists": map[string]any{"field": path}}
}

// andQuery conjoins the children that narrow into a bool.must, dropping any that do
// not (an AND with a match-everything term is just the rest). It narrows if at least
// one child does.
func (s *Store) andQuery(and predicate.And) (map[string]any, bool) {
	var parts []any
	for _, child := range and {
		if q, ok := s.toQuery(child); ok {
			parts = append(parts, q)
		}
	}
	switch len(parts) {
	case 0:
		return nil, false
	case 1:
		return parts[0].(map[string]any), true
	default:
		return map[string]any{"bool": map[string]any{"must": parts}}, true
	}
}

// orQuery disjoins the children into a bool.should. An OR narrows only if every
// branch does: a branch that cannot be pushed matches everything, which would make
// the whole OR match everything, so a single unpushable branch drops it to a full
// scan.
func (s *Store) orQuery(or predicate.Or) (map[string]any, bool) {
	parts := make([]any, 0, len(or))
	for _, child := range or {
		q, ok := s.toQuery(child)
		if !ok {
			return nil, false
		}
		parts = append(parts, q)
	}
	if len(parts) == 0 {
		return nil, false
	}
	return map[string]any{"bool": map[string]any{"should": parts, "minimum_should_match": 1}}, true
}

// field renders a predicate path as a dotted Elasticsearch field reference.
func field(path []string) string {
	return strings.Join(path, ".")
}

// compile-time assertion that Store satisfies the optional filtered-scan capability.
var _ interface {
	ScanFiltered(context.Context, predicate.Node, func(map[string]any) error) error
} = (*Store)(nil)

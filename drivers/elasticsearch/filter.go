package elasticsearch

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/rawpred"
)

// fieldClass is the literal kind a mapped field can match exactly with a term query.
type fieldClass int

const (
	stringClass fieldClass = iota // keyword, ip, or a text field's keyword sub-field
	numberClass                   // an integer or floating mapping type
	boolClass                     // boolean
)

// exactField is a top-level field that an equality can push as a term query. It
// holds the concrete term path (the field, or its .keyword sub-field for a text
// field) and the literal class that the field accepts.
type exactField struct {
	path  string
	class fieldClass
}

// mappingProperty is the subset of an index-mapping property that the driver
// reads. It holds the type and any multi-fields (the keyword sub-field of a text
// field). It also holds whether the property is an object (has nested
// properties, so not a scalar term target).
type mappingProperty struct {
	Type       string                     `json:"type"`
	Fields     map[string]mappingProperty `json:"fields"`
	Properties map[string]json.RawMessage `json:"properties"`
}

// exact reports whether a top-level field named name can back an exact term
// match, and how. A keyword, numeric, boolean, or ip field matches on itself. A
// text field matches on its keyword sub-field when it has one. Everything else
// cannot be pushed safely: object, nested, date, geo, or a text field with no
// keyword sub-field.
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

// exactFieldsFrom reduces a GET _mapping reply to the top-level fields where an
// equality can push as a term query. If the index resolves to several mappings
// (an alias), a field stays only if all mappings agree that it is exact and of
// one class. This keeps a term off a field that is analyzed text, or a different
// exact type, in part of the fan-out.
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

// ScanFiltered lets the server narrow the scan with a bool query for pred. See
// query.FilteredScanner.
//
// If pred has no safe query, the scan is a plain point-in-time walk. This happens
// for a range, regex, length, negation, or an equality on a field that the mapping
// does not make exactly matchable.
//
// A client-side rawpred prefilter drops hits that the full predicate provably
// rejects, before the decode. It is off in two cases. First, the server query
// already captures pred exactly. Second, pred references the injected _id field.
// The raw _source does not carry _id, so rawpred reads a clean absence and can
// wrongly drop a document whose id matches.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	if s.index == "" {
		return errNoIndex
	}
	// A nil query is a full point-in-time walk, so the narrowing flag needs no branch.
	query, _ := s.toQuery(pred)
	return s.pagedSearch(ctx, query, s.prefilterMatcher(pred), fn)
}

// prefilterMatcher makes the live prefilter decision with the mapping. A nil matcher
// means no prefilter.
func (s *Store) prefilterMatcher(pred predicate.Node) *rawpred.Matcher {
	if s.exactPush(pred) || referencesInjectedID(pred) {
		return nil
	}
	return rawpred.NewMatcher(pred, s.decimal)
}

// neverExact reports whether the predicate has a leaf that no mapping makes
// exact. Such a leaf is not an Eq, is an Eq with a null value, or is an Eq on a
// nested path. An empty composite counts as never exact. The function mirrors
// exactPush without the mapping.
func neverExact(n predicate.Node) bool {
	switch t := n.(type) {
	case predicate.Eq:
		return t.Value == nil || len(t.Path) != 1
	case predicate.And:
		return anyNeverExact(t)
	case predicate.Or:
		return anyNeverExact(t)
	default:
		return true
	}
}

// anyNeverExact reports whether a composite is empty or has a child that is never
// exact.
func anyNeverExact(children []predicate.Node) bool {
	return len(children) == 0 || slices.ContainsFunc(children, neverExact)
}

// exactPush reports whether toQuery captures n exactly. The backend then returns
// precisely the jq matches, so a client-side prefilter only re-confirms them.
// Only an equality pushed as a term query is exact. Existence, non-existence,
// ranges, negations, and a dropped conjunct leave the backend over- or
// under-inclusive, so they keep the prefilter. An And or Or is exact only if every
// child is.
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

// allExact reports whether every child of a non-empty composite is exactly
// pushable. An empty composite is never exact.
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

// referencesInjectedID reports whether any field path in the predicate tree is
// rooted at the injected _id field. decodeSource injects that field. It is not in
// the raw _source, so rawpred reads it as absent and can wrongly reject a
// match. In that case ScanFiltered turns the prefilter off for the whole scan. The
// walk includes the element conditions of ElemMatch and NoneMatch.
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
	return slices.ContainsFunc(children, referencesInjectedID)
}

// rootedAtID reports whether a field path's first segment is the injected _id field.
func rootedAtID(path []string) bool {
	return len(path) > 0 && path[0] == injectedIDField
}

// toQuery translates a neutral predicate into an Elasticsearch query. The second
// return is false if the query does not narrow the scan. In that case the node,
// or an unsafe part of it, cannot be pushed without the risk of wrongly
// excluding a jq match. Only pure-superset nodes are pushed: equality on an
// exactly-matchable field and existence. An Elasticsearch range excludes a
// missing field and orders types differently from jq, so ranges stay on the
// client. So do negations and the polymorphic length, regex, and any tests.
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

// eqQuery builds a term query for an equality. It builds the query only when
// two things hold. The field is a single top-level scalar that the mapping makes
// exactly matchable. The type of the literal matches the class of the field. It
// never pushes a null literal. jq reads a missing field as null, so `== null`
// matches absent-or-null, which no single term query reproduces safely.
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

// matchesLiteral reports whether the Go type of a pushdown literal matches the
// class of the field. So a term is only pushed when Elasticsearch does not reject
// or coerce it. A mismatch (for example a string literal against a numeric
// field) is left client-side. There jq also treats the cross-type comparison as
// unequal.
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

// andQuery conjoins the children that narrow into a bool.must, dropping any that
// do not. An AND with a match-everything term is the same as the rest. The
// result narrows if at least one child does.
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

// orQuery disjoins the children into a `bool.should`. An OR narrows only if
// every branch does. A branch that cannot be pushed matches everything. That
// makes the whole OR match everything. So a single unpushable branch drops it to
// a full scan.
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

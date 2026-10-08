// Package shape reduces a sample of items to an inferred structural shape and
// projects it two ways: Comparable, a flat path->node map fed to a structural
// diff, and JSONSchema, a draft 2020-12 document. NoSQL stores declare no schema, so
// the shape is sampled and inferred, never authoritative — a wider sample yields
// a truer shape. It is driver-agnostic and holds no I/O: callers read each side
// through the query ports and hand the materialized values here.
//
// Inference heuristics adapted from quicktype (github.com/glideapps/quicktype),
// Apache-2.0; no code copied.
package shape

import (
	"encoding/json"
	"math"
	"math/big"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Shape is the accumulated result of Infer over a sample: a tree of nodes rooted
// at the whole collection. The accumulation is the single source of truth per
// fact; Comparable and JSONSchema are pure projections of it.
type Shape struct {
	root *node
}

// node accumulates what was observed at one position in the shape tree: the
// distinct JSON kinds seen there, how many values landed there (seen), how many
// of those were objects (objCount — the denominator for this node's fields'
// presence), the object children, the unified array-element child (elem), and,
// for string values, all-or-nothing format tracking. Because every tree position
// maps to a unique path, presence lives on the edge as the child's seen count
// measured against its parent's objCount.
type node struct {
	kinds    kindSet
	seen     int
	objCount int
	fields   map[string]*node
	elem     *node
	mapVal   *node
	fmt      formats
	enum     enumAcc
}

// newNode returns an empty node ready to accumulate.
func newNode() *node {
	return &node{kinds: kindSet{}}
}

// field returns the child node for the object field name. It creates the child,
// and the fields map, on first use.
func (n *node) field(name string) *node {
	if n.fields == nil {
		n.fields = map[string]*node{}
	}
	child := n.fields[name]
	if child == nil {
		child = newNode()
		n.fields[name] = child
	}
	return child
}

// elemNode returns the unified array-element child. It creates the child on first
// use.
func (n *node) elemNode() *node {
	if n.elem == nil {
		n.elem = newNode()
	}
	return n.elem
}

// Infer reduces a set of sampled items into a Shape. Each item is accumulated at
// the root; object items descend into their fields, building the tree. The map
// key (the store's key) is not part of the value shape and is not walked.
func Infer(items map[string]any) *Shape {
	root := newNode()
	for _, v := range items {
		accumulate(root, v)
	}
	inferMaps(root, true)
	return &Shape{root: root}
}

// Map-detection thresholds and enum thresholds, kept together as the one place to
// tune inference. Map detection collapses an object path into a map when it has
// enough object instances (mapMinInstances), enough distinct child keys
// (mapMinKeys), and those keys are mostly unique across the key-slots seen
// (mapMinDistinctRatio) — the signature of id-keyed data, not a fixed record
// whose few keys repeat in every instance. The ratio is the real discriminator;
// a wide fixed record clears the count gates but its keys repeat, so its ratio is
// low. Repeat-heavy maps below the ratio are conservatively left literal.
const (
	mapMinInstances     = 8
	mapMinKeys          = 8
	mapMinDistinctRatio = 0.75

	enumMinInstances = 16
	enumMaxDistinct  = 8
	enumMaxRatio     = 0.25
)

// inferMaps collapses id-keyed object nodes into maps, depth-first. The root is
// never collapsed: a genuinely heterogeneous top-level collection folding to one
// map value would hide everything. A collapsed node merges its literal children
// into a single map-value node (path segment "{}"), drops the literal child
// paths, and reads as kind map; recursion continues into the merged value, so a
// nested id-keyed map is still detected.
func inferMaps(n *node, root bool) {
	if !root && shouldCollapse(n) {
		mapVal := newNode()
		for _, child := range n.fields {
			mergeNode(mapVal, child)
		}
		n.mapVal = mapVal
		n.fields = nil
		n.kinds.replace(kindObject, kindMap)
		inferMaps(mapVal, false)
		return
	}
	for _, child := range n.fields {
		inferMaps(child, false)
	}
	if n.elem != nil {
		inferMaps(n.elem, false)
	}
}

// shouldCollapse reports whether an object node's children look id-keyed enough to
// be a map. slots is the total key-slots seen (Σ child presence); distinct/slots
// is the fraction of keys that are distinct.
func shouldCollapse(n *node) bool {
	distinct := len(n.fields)
	if n.objCount < mapMinInstances || distinct < mapMinKeys {
		return false
	}
	slots := 0
	for _, child := range n.fields {
		slots += child.seen
	}
	return float64(distinct)/float64(slots) >= mapMinDistinctRatio
}

// mergeNode folds src's whole subtree into dst: union of kinds, summed seen and
// object counts, merged formats, and recursively merged fields and elements. It
// is how a collapsing node's id-keyed children combine into one map-value shape,
// so the value's own fields carry presence against the merged object count.
func mergeNode(dst, src *node) {
	for k := range src.kinds {
		dst.kinds.add(k)
	}
	dst.seen += src.seen
	dst.objCount += src.objCount
	dst.fmt.mergeFrom(src.fmt)
	dst.enum.mergeFrom(src.enum)
	for name, sc := range src.fields {
		mergeNode(dst.field(name), sc)
	}
	if src.elem != nil {
		mergeNode(dst.elemNode(), src.elem)
	}
}

// accumulate records value v at node n and recurses into an object's fields and
// an array's elements. A field's presence is counted against n.objCount: each
// object occurrence visits each of its keys once, so a child's seen count over
// the parent's object count is exactly the fraction of parent instances that
// carried the field — parent-relative, so an optional parent never makes an
// always-present child look optional. Array elements unify into a single elem
// node (path segment "[]"), so the path space stays bounded by the sample, not by
// element count; an object element's fields then count against the elements seen.
// An empty array records the array kind and nothing below it.
func accumulate(n *node, v any) {
	n.seen++
	n.kinds.add(kindOf(v))
	switch t := v.(type) {
	case string:
		n.fmt.observe(t)
		n.enum.observe(t)
	case map[string]any:
		n.objCount++
		for k, val := range t {
			accumulate(n.field(k), val)
		}
	case []any:
		for _, e := range t {
			accumulate(n.elemNode(), e)
		}
	}
}

// Comparable projects the shape to a flat path->node map: "$root" plus every
// dotted field path, each node carrying its sorted distinct types and (for
// fields) a presence of "required" or "optional". Presence is a stable attribute,
// not a sample-count fraction, so two independently drawn samples of the same
// schema project identically and a schema diff stays quiet under resampling. The
// result is a map[string]any, so it runs through the structural Tree like any
// other value.
func (s *Shape) Comparable() map[string]any {
	out := map[string]any{"$root": map[string]any{"types": s.root.comparableTypes()}}
	projectComparable(out, "", s.root)
	return out
}

// projectComparable emits one node per field and array element of parent under
// prefix, recursing to build dotted paths with "[]" array-element segments. A
// field carries presence (against the parent's object count); the "[]" element
// node is typed only — the object fields under it carry the presence.
func projectComparable(out map[string]any, prefix string, parent *node) {
	for name, child := range parent.fields {
		path := prefix + "." + name
		out[path] = map[string]any{
			"types":    child.comparableTypes(),
			"presence": presence(child.seen, parent.objCount),
		}
		projectComparable(out, path, child)
	}
	if parent.elem != nil {
		path := prefix + "[]"
		out[path] = map[string]any{"types": parent.elem.comparableTypes()}
		projectComparable(out, path, parent.elem)
	}
	if parent.mapVal != nil {
		path := prefix + "{}"
		out[path] = map[string]any{"types": parent.mapVal.comparableTypes()}
		projectComparable(out, path, parent.mapVal)
	}
}

// comparableTypes renders a node's kind set as the sorted []any the shape nodes
// and the structural diff consume. A field that saw both integer and number
// collapses to "number" alone (integer ∪ double = double); an all-or-nothing
// string format renders into the type name, so "string(date-time)" reads as one
// value and format drift is a single visible row.
func (n *node) comparableTypes() []any {
	format := n.fmt.resolve()
	names := make([]string, 0, len(n.kinds))
	for _, k := range n.kinds.collapsed() {
		names = append(names, comparableName(k, format))
	}
	sort.Strings(names)
	out := make([]any, len(names))
	for i, name := range names {
		out[i] = name
	}
	return out
}

// comparableName renders kind k in the Comparable vocabulary. A string with a
// resolved format reads "string(format)".
func comparableName(k kind, format string) string {
	if k == kindString && format != "" {
		return "string(" + format + ")"
	}
	return k.String()
}

// presence classifies a child seen in seen of total parent object-instances:
// "required" when it appeared in every one, "optional" otherwise.
func presence(seen, total int) string {
	if seen == total {
		return "required"
	}
	return "optional"
}

// JSONSchema projects the shape to one JSON Schema draft 2020-12 document titled
// title: $schema and title, then the root value schema. Object properties carry
// per-object required arrays, strings carry format and a low-cardinality enum,
// maps carry additionalProperties, and arrays carry items. A non-object root is
// legal (a string keyspace yields {"type":"string"}); a mixed root uses a type
// array. It describes values only — key shape is the driver's display concern.
func (s *Shape) JSONSchema(title string) map[string]any {
	doc := schemaFor(s.root)
	doc["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	doc["title"] = title
	return doc
}

// schemaFor renders one node as a JSON Schema object, attaching the detail of
// each kind it holds: additionalProperties for a map, properties+required for a
// plain object, items for an array, and format/enum for a string. Integer
// collapses into number when both are present; an unknown-only node has no type.
func schemaFor(n *node) map[string]any {
	schema := map[string]any{}
	setType(schema, n.schemaTypes())

	switch {
	case n.mapVal != nil:
		schema["additionalProperties"] = schemaFor(n.mapVal)
	case n.kinds.has(kindObject) && len(n.fields) > 0:
		setProperties(schema, n)
	}
	if n.kinds.has(kindArray) && n.elem != nil {
		schema["items"] = schemaFor(n.elem)
	}
	if n.kinds.has(kindString) {
		setStringDetail(schema, n)
	}
	return schema
}

// setProperties adds properties, and required when a field is in every object, to
// the schema of an object node.
func setProperties(schema map[string]any, n *node) {
	props := make(map[string]any, len(n.fields))
	var required []string
	for name, child := range n.fields {
		props[name] = schemaFor(child)
		if child.seen == n.objCount {
			required = append(required, name)
		}
	}
	schema["properties"] = props
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = toAnySlice(required)
	}
}

// setStringDetail adds format and enum to the schema of a node that holds strings.
func setStringDetail(schema map[string]any, n *node) {
	if f := n.fmt.resolve(); f != "" {
		schema["format"] = f
	}
	if len(n.kinds) == 1 { // enum only where every value is a string
		if vals := n.enum.values(); vals != nil {
			schema["enum"] = toAnySlice(vals)
		}
	}
}

// setType sets the JSON Schema "type": a bare string for a single type, a type
// array for several, and nothing for an unknown-only node.
func setType(schema map[string]any, types []string) {
	switch len(types) {
	case 0:
	case 1:
		schema["type"] = types[0]
	default:
		schema["type"] = toAnySlice(types)
	}
}

// schemaTypes renders a node's kind set as sorted JSON Schema type names, with
// integer collapsed into number when both are present and object/map both
// rendering "object" (a map is a JSON object with additionalProperties).
func (n *node) schemaTypes() []string {
	names := make([]string, 0, len(n.kinds))
	for _, k := range n.kinds.collapsed() {
		if name := k.schemaName(); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	// Object and map both render "object", so drop the repeat.
	return slices.Compact(names)
}

// toAnySlice copies a string slice into an []any for JSON rendering.
func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// kindSet is the set of distinct JSON kinds observed at a node.
type kindSet map[kind]struct{}

// add records a kind.
func (s kindSet) add(k kind) { s[k] = struct{}{} }

// has reports whether k was observed.
func (s kindSet) has(k kind) bool { _, ok := s[k]; return ok }

// collapsed returns the kinds in ascending order. Integer is left out when number
// is also present, because integer joined with number is number.
func (s kindSet) collapsed() []kind {
	hasNumber := s.has(kindNumber)
	kinds := make([]kind, 0, len(s))
	for k := range s {
		if k == kindInteger && hasNumber {
			continue
		}
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	return kinds
}

// replace swaps one kind for another (object -> map on collapse), leaving any
// other observed kinds intact.
func (s kindSet) replace(from, to kind) {
	if _, ok := s[from]; ok {
		delete(s, from)
		s[to] = struct{}{}
	}
}

// kind is a canonical JSON kind. It splits integer from number and reserves map
// for the id-keyed-map post-pass; each projection renders it in its own
// vocabulary (Comparable: "bool"/"integer"; JSON Schema: "boolean"/"integer").
type kind int

const (
	kindNull kind = iota
	kindBool
	kindInteger
	kindNumber
	kindString
	kindArray
	kindObject
	kindMap
	kindUnknown
)

// kindNames maps each kind to its name in the Comparable vocabulary.
var kindNames = map[kind]string{
	kindNull:    "null",
	kindBool:    "bool",
	kindInteger: "integer",
	kindNumber:  "number",
	kindString:  "string",
	kindArray:   "array",
	kindObject:  "object",
	kindMap:     "map",
}

// String renders a kind in the Comparable vocabulary.
func (k kind) String() string {
	if name, ok := kindNames[k]; ok {
		return name
	}
	return "unknown"
}

// schemaName renders a kind in the JSON Schema vocabulary: "bool" becomes
// "boolean", a map is a JSON "object" (with additionalProperties), and an unknown
// kind contributes no type name.
func (k kind) schemaName() string {
	switch k {
	case kindNull:
		return "null"
	case kindBool:
		return "boolean"
	case kindInteger:
		return "integer"
	case kindNumber:
		return "number"
	case kindString:
		return "string"
	case kindArray:
		return "array"
	case kindObject, kindMap:
		return "object"
	default:
		return ""
	}
}

// kindOf classifies a normalized value. Integers (every Go int/uint width plus
// *big.Int for values past int64, and any float or JSON number holding an
// integral value) are "integer"; a fractional float or JSON number is "number".
// Integral-value counting makes the split uniform whether a driver hands an int
// or a JSON-decoded float64 for the same 5.
func kindOf(v any) kind {
	switch v.(type) {
	case nil:
		return kindNull
	case bool:
		return kindBool
	case string:
		return kindString
	case []any:
		return kindArray
	case map[string]any:
		return kindObject
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, uintptr,
		*big.Int:
		return kindInteger
	default:
		return numericKind(v)
	}
}

// numericKind classifies a float32, float64 or json.Number value. Any other type
// is kindUnknown.
func numericKind(v any) kind {
	switch t := v.(type) {
	case float32:
		return floatKind(float64(t))
	case float64:
		return floatKind(t)
	case json.Number:
		return numberKind(t)
	default:
		return kindUnknown
	}
}

// floatKind splits a float into integer (a finite, integral value) or number. NaN
// is number without a separate test, because NaN never equals its own Trunc.
func floatKind(f float64) kind {
	if !math.IsInf(f, 0) && f == math.Trunc(f) {
		return kindInteger
	}
	return kindNumber
}

// numberKind splits a json.Number (from a decoder configured with UseNumber that
// reached Infer unconverted) into integer or number. An exact integer literal is
// integer; otherwise its float value decides by integrality.
func numberKind(n json.Number) kind {
	if !strings.ContainsAny(n.String(), ".eE") {
		return kindInteger
	}
	f, err := n.Float64()
	if err != nil {
		return kindNumber
	}
	return floatKind(f)
}

// Format matchers. The three are structurally disjoint (a bare date has no "T",
// so it fails RFC3339; a date-time fails the date anchor; a UUID matches neither),
// so at most one survives the all-or-nothing intersection.
var (
	reDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// matchDateTime reports whether s is an RFC3339 date-time.
func matchDateTime(s string) bool {
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}

// matchDate reports whether s is a YYYY-MM-DD calendar date (a real one — the
// regexp gates the shape, time.Parse rejects 2006-13-40).
func matchDate(s string) bool {
	if !reDate.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// matchUUID reports whether s is an 8-4-4-4-12 hex UUID.
func matchUUID(s string) bool { return reUUID.MatchString(s) }

// formats tracks, for one node, whether every string seen so far matched a given
// format — quicktype's all-or-nothing transform rule: one mismatch drops the
// format for the field.
type formats struct {
	hasString bool
	dateTime  bool
	date      bool
	uuid      bool
}

// observe folds one string into the all-or-nothing state.
func (f *formats) observe(s string) {
	m, d, u := matchDateTime(s), matchDate(s), matchUUID(s)
	if !f.hasString {
		f.hasString, f.dateTime, f.date, f.uuid = true, m, d, u
		return
	}
	f.dateTime = f.dateTime && m
	f.date = f.date && d
	f.uuid = f.uuid && u
}

// mergeFrom folds another node's format state into f, keeping a format only if it
// held for every string across both sides — the all-or-nothing rule extended over
// a map collapse. A side that saw no string contributes nothing.
func (f *formats) mergeFrom(o formats) {
	if !o.hasString {
		return
	}
	if !f.hasString {
		*f = o
		return
	}
	f.dateTime = f.dateTime && o.dateTime
	f.date = f.date && o.date
	f.uuid = f.uuid && o.uuid
}

// resolve returns the single surviving format ("date-time"/"date"/"uuid"), or ""
// when no string was seen or none held for every string. Priority orders the
// (disjoint) formats deterministically.
func (f *formats) resolve() string {
	if !f.hasString {
		return ""
	}
	switch {
	case f.dateTime:
		return "date-time"
	case f.date:
		return "date"
	case f.uuid:
		return "uuid"
	default:
		return ""
	}
}

// enumAcc tracks, for one node, the count of string values seen and the distinct
// ones (capped at the enum ceiling), so a low-cardinality string field can
// project an enum. Once the distinct count exceeds the ceiling it stops
// collecting and marks itself over — past that it cannot be an enum.
type enumAcc struct {
	count int
	vals  map[string]struct{}
	over  bool
}

// observe folds one string value in, capping the distinct set.
func (e *enumAcc) observe(s string) {
	e.count++
	if e.over {
		return
	}
	if e.vals == nil {
		e.vals = map[string]struct{}{}
	}
	e.vals[s] = struct{}{}
	if len(e.vals) > enumMaxDistinct {
		e.over, e.vals = true, nil
	}
}

// mergeFrom folds another node's enum state in (a map collapse), re-capping the
// distinct set.
func (e *enumAcc) mergeFrom(o enumAcc) {
	e.count += o.count
	if e.over || o.over {
		e.over, e.vals = true, nil
		return
	}
	for v := range o.vals {
		if e.vals == nil {
			e.vals = map[string]struct{}{}
		}
		e.vals[v] = struct{}{}
	}
	if len(e.vals) > enumMaxDistinct {
		e.over, e.vals = true, nil
	}
}

// values returns the node's sorted enum values when it qualifies — at least
// enumMinInstances strings, not over the distinct ceiling, and a distinct/count
// ratio no greater than enumMaxRatio — else nil.
func (e *enumAcc) values() []string {
	if e.over || e.count < enumMinInstances {
		return nil
	}
	if float64(len(e.vals))/float64(e.count) > enumMaxRatio {
		return nil
	}
	vals := make([]string, 0, len(e.vals))
	for v := range e.vals {
		vals = append(vals, v)
	}
	sort.Strings(vals)
	return vals
}

package diff

import (
	"sort"
	"strconv"
)

// Infer reduces a set of sampled items into a comparable shape: the JSON type of
// each item at "$root", and for object items every field path (dotted, jq-style,
// e.g. ".addr.city") mapped to the set of types seen there and how often the
// field is present. NoSQL stores declare no schema, so the shape is sampled and
// inferred, never authoritative — a wider sample yields a truer shape. The result
// is itself a map[string]any, so schema diff runs it through Tree like any other
// value.
func Infer(items map[string]any) map[string]any {
	total := len(items)
	rootTypes := map[string]struct{}{}
	fields := map[string]*fieldAcc{}
	for _, v := range items {
		rootTypes[jsonType(v)] = struct{}{}
		if m, ok := v.(map[string]any); ok {
			descend(m, "", fields)
		}
	}
	shape := map[string]any{"$root": summary(rootTypes, total)}
	for path, a := range fields {
		shape["."+path] = summary(a.types, total, a.present)
	}
	return shape
}

// fieldAcc accumulates the observed types (a set — only the distinct types
// matter) and the presence count for one field path.
type fieldAcc struct {
	types   map[string]struct{}
	present int
}

// descend records every field of m under prefix and recurses into nested object
// fields, building dotted paths. An object field records both its own "object"
// type and its children; arrays are leaves (type "array"), not walked, since
// element shapes vary and flattening them would explode the path space.
func descend(m map[string]any, prefix string, fields map[string]*fieldAcc) {
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		a := fields[path]
		if a == nil {
			a = &fieldAcc{types: map[string]struct{}{}}
			fields[path] = a
		}
		a.types[jsonType(v)] = struct{}{}
		a.present++
		if child, ok := v.(map[string]any); ok {
			descend(child, path, fields)
		}
	}
}

// summary renders a type tally into the shape node: the sorted distinct types
// and, when a presence count is given, "present/total". Callers pass the field's
// present count; $root omits it (every item is present at the root).
func summary(types map[string]struct{}, total int, present ...int) map[string]any {
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	sorted := make([]any, len(names))
	for i, name := range names {
		sorted[i] = name
	}
	node := map[string]any{"types": sorted}
	if len(present) > 0 {
		node["presence"] = strconv.Itoa(present[0]) + "/" + strconv.Itoa(total)
	}
	return node
}

// jsonType names the JSON type of a normalized value.
func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case int, int32, int64, float32, float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

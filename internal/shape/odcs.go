package shape

import "sort"

// ODCS logical-type constants and the date format patterns iq attaches. The
// Open Data Contract Standard v3.1.0 admits nine logical types with no binary or
// decimal member, so iq's base64-binary and exact-decimal strings — which the
// shape lattice already carries as plain strings — stay logicalType string. The
// date patterns are JDK DateTimeFormatter strings (ODCS's date/timestamp format
// vocabulary), the mechanical equivalent of iq's RFC3339 date-time and calendar
// date formats.
const (
	odcsTypeString  = "string"
	odcsTypeInteger = "integer"
	odcsTypeNumber  = "number"
	odcsTypeBoolean = "boolean"
	odcsTypeObject  = "object"
	odcsTypeArray   = "array"
	odcsTypeDate    = "date"

	odcsFormatDateTime = "yyyy-MM-dd'T'HH:mm:ssXXX" // JDK pattern for RFC3339.
	odcsFormatDate     = "yyyy-MM-dd"               // JDK pattern for a calendar date.
	odcsFormatUUID     = "uuid"                     // ODCS string format enum member.
)

// ODCSSchemaObject projects the shape to one ODCS v3.1.0 schema object named
// name — the root element rendered in ODCS's vocabulary: logicalType per node,
// nested objects via a properties array, arrays via an items element, and
// per-property required flags from parent-relative presence. It is a pure
// projection of the same inferred tree JSONSchema renders, so the two stay
// consistent per fact. A non-object root is legal: a string keyspace yields a
// schema object with logicalType string and no properties.
func (s *Shape) ODCSSchemaObject(name string) map[string]any {
	return s.root.odcsElement(name)
}

// odcsElement renders one node as an ODCS element (a schema object at the root, a
// property or an array item below it). name is the element identifier; the empty
// string omits it, which is how an array item — a nameless element definition —
// is rendered. A node carrying named fields descends into a properties array; one
// carrying a unified array element descends into an items element. Both guards are
// on the child slot itself, not a redundant kind check: only an object populates
// fields (a collapsed map nils them, so it renders as a bare object — ODCS v3.1.0
// has no additionalProperties analog), and only a non-empty array populates elem
// (an empty array leaves it nil, so it yields no items).
func (n *node) odcsElement(name string) map[string]any {
	el := map[string]any{}
	if name != "" {
		el["name"] = name
	}
	if lt, opts := n.odcsLogicalType(); lt != "" {
		el["logicalType"] = lt
		if len(opts) > 0 {
			el["logicalTypeOptions"] = opts
		}
	}
	if len(n.fields) > 0 {
		el["properties"] = n.odcsProperties()
	}
	if n.elem != nil {
		el["items"] = n.elem.odcsElement("")
	}
	return el
}

// odcsProperties renders a node's object fields as a sorted array of ODCS
// property elements. Order is fixed by field name so the contract is
// deterministic (an ODCS properties list is an array, whose order an encoder
// preserves — unlike a map, whose keys it sorts). A field present in every object
// instance carries required: true; an optional field omits it, taking ODCS's
// default of false.
func (n *node) odcsProperties() []any {
	names := make([]string, 0, len(n.fields))
	for name := range n.fields {
		names = append(names, name)
	}
	sort.Strings(names)
	props := make([]any, 0, len(names))
	for _, name := range names {
		child := n.fields[name]
		p := child.odcsElement(name)
		if child.seen == n.objCount {
			p["required"] = true
		}
		props = append(props, p)
	}
	return props
}

// odcsLogicalType maps a node's kind set to a single ODCS logical type and its
// options. ODCS has no union type, so a fixed priority picks one type for a
// mixed node: composite kinds first (object/map, then array), then string, then
// number over integer (matching JSONSchema's integer-into-number collapse), then
// boolean. A string carries a format option demoted from iq's inferred format:
// date-time and date become logicalType date with a JDK date pattern, a UUID
// stays logicalType string with the ODCS uuid format. A null-only or unknown node
// has no ODCS logical type — nullability is expressed by the absence of required,
// not a type.
func (n *node) odcsLogicalType() (string, map[string]any) {
	switch {
	case n.kinds.has(kindObject) || n.kinds.has(kindMap):
		return odcsTypeObject, nil
	case n.kinds.has(kindArray):
		return odcsTypeArray, nil
	case n.kinds.has(kindString):
		switch n.fmt.resolve() {
		case "date-time":
			return odcsTypeDate, map[string]any{"format": odcsFormatDateTime}
		case "date":
			return odcsTypeDate, map[string]any{"format": odcsFormatDate}
		case "uuid":
			return odcsTypeString, map[string]any{"format": odcsFormatUUID}
		default:
			return odcsTypeString, nil
		}
	case n.kinds.has(kindNumber):
		return odcsTypeNumber, nil
	case n.kinds.has(kindInteger):
		return odcsTypeInteger, nil
	case n.kinds.has(kindBool):
		return odcsTypeBoolean, nil
	default:
		return "", nil
	}
}

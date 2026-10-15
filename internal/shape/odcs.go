package shape

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ODCS logical-type constants and the date format patterns that iq attaches. The
// Open Data Contract Standard v3.2.0 admits eleven logical types. iq emits nine of
// them. It never emits vector, because the values of a sample cannot prove an
// embedding. It has no binary or decimal member. iq's base64-binary and
// exact-decimal strings are plain strings in the shape lattice, so they stay
// logicalType string. The date pattern is a JDK DateTimeFormatter string (ODCS's
// date format vocabulary). It matches iq's calendar date format. A date-time
// string becomes logicalType timestamp with no format option, because a JDK
// pattern cannot express the variable-width fraction that RFC3339 allows.
const (
	odcsTypeString    = "string"
	odcsTypeInteger   = "integer"
	odcsTypeNumber    = "number"
	odcsTypeBoolean   = "boolean"
	odcsTypeObject    = "object"
	odcsTypeArray     = "array"
	odcsTypeMap       = "map"
	odcsTypeDate      = "date"
	odcsTypeTimestamp = "timestamp"

	odcsFormatDate = "yyyy-MM-dd" // JDK pattern for a calendar date.
	odcsFormatUUID = "uuid"       // ODCS string format enum member.
)

// ErrODCSNotRecords reports a root that holds values other than objects and nulls.
// An ODCS schema object allows only logicalType object, so such a root has no
// valid contract.
var ErrODCSNotRecords = errors.New("odcs needs objects at the root")

// ODCSContract wraps the ODCS schema object of the shape in an ODCS v3.2.0
// fundamentals envelope. Every field is deterministic, with no timestamps or
// random ids. The id, name, and schema-object name all take the argument name.
// Version and status take fixed initial values. The caller must derive name from
// the source handle, never from the URL, so no stored credential can reach the
// contract. If the root holds anything but objects and nulls, the error wraps
// ErrODCSNotRecords.
func (s *Shape) ODCSContract(name string) (map[string]any, error) {
	obj, err := s.odcsSchemaObject(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"apiVersion": "v3.2.0",
		"kind":       "DataContract",
		"id":         name,
		"name":       name,
		"version":    "1.0.0",
		"status":     "draft",
		"schema":     []any{obj},
	}, nil
}

// odcsSchemaObject projects the shape to one ODCS v3.2.0 schema object named
// name, the root element in ODCS's vocabulary. The projection sets logicalType per
// node, nested objects via a properties array, maps via a map element, arrays via
// an items element, and per-property required flags. It uses the same inferred
// tree as JSONSchema, so the two stay consistent per fact. A root that holds a
// kind other than object and null has no valid schema object, so the function
// returns ErrODCSNotRecords.
func (s *Shape) odcsSchemaObject(name string) (map[string]any, error) {
	var others []string
	for k := range s.root.kinds {
		if k != kindObject && k != kindNull {
			others = append(others, k.String())
		}
	}
	if len(others) > 0 {
		sort.Strings(others)
		return nil, fmt.Errorf("the root holds %s values: %w", strings.Join(others, " and "), ErrODCSNotRecords)
	}
	return s.root.odcsElement(name), nil
}

// odcsElement renders one node as an ODCS element. The element is a schema object
// at the root, or a property or an array item below it. The name argument is the
// element identifier. The empty string omits it, which is how an array item (a
// nameless element definition) is rendered. The function picks one logical type,
// then emits only the composite slot of that type. An object descends into a
// properties array, a map into a map element, and an array into an items element.
// Each slot is emitted only when its child exists. An empty object has no fields.
// An empty array has no element. A string node with an inferred enum carries the
// enum values.
func (n *node) odcsElement(name string) map[string]any {
	el := map[string]any{}
	if name != "" {
		el["name"] = name
	}
	lt, opts := n.odcsLogicalType()
	setODCSType(el, lt, opts)
	n.setODCSComposite(el, lt)
	if enum := n.odcsEnum(); enum != nil {
		el["enum"] = enum
	}
	return el
}

// setODCSType sets the logical type of el and its options. An empty type sets
// nothing. Empty options set no logicalTypeOptions key.
func setODCSType(el map[string]any, lt string, opts map[string]any) {
	if lt == "" {
		return
	}
	el["logicalType"] = lt
	if len(opts) > 0 {
		el["logicalTypeOptions"] = opts
	}
}

// setODCSComposite sets the composite slot of the logical type lt on el. An
// object gets a properties array, a map gets a map element with a string key, and
// an array gets an items element. Other types get no slot. An empty object has no
// fields, and an empty array has no element, so neither gets a slot.
func (n *node) setODCSComposite(el map[string]any, lt string) {
	switch lt {
	case odcsTypeObject:
		if len(n.fields) > 0 {
			el["properties"] = n.odcsProperties()
		}
	case odcsTypeMap:
		el["map"] = map[string]any{
			"key":   map[string]any{"logicalType": odcsTypeString},
			"value": n.mapVal.odcsElement(""),
		}
	case odcsTypeArray:
		if n.elem != nil {
			el["items"] = n.elem.odcsElement("")
		}
	}
}

// odcsEnum renders the inferred enum of a string node as ODCS enum values, one
// {value: v} object per value. A node with no enum returns nil.
func (n *node) odcsEnum() []any {
	vals := n.stringEnum()
	if vals == nil {
		return nil
	}
	enum := make([]any, len(vals))
	for i, v := range vals {
		enum[i] = map[string]any{"value": v}
	}
	return enum
}

// odcsProperties renders a node's object fields as a sorted array of ODCS
// property elements. Order is fixed by field name so the contract is
// deterministic. An ODCS properties list is an array, whose order an encoder
// preserves. A map is different, because an encoder sorts its keys. ODCS required
// means that the element never holds null. A field carries required: true when it
// is present in every object instance and no null was observed. An optional or
// nullable field omits it, taking ODCS's default of false.
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
		if child.seen == n.objCount && !child.kinds.has(kindNull) {
			p["required"] = true
		}
		props = append(props, p)
	}
	return props
}

// odcsLogicalType maps a node's kind set to a single ODCS logical type and its
// options. ODCS has no union type, so a fixed priority picks one type for a mixed
// node. The order is: composite kinds first (map, then object, then array), then
// string, then number over integer (matching JSONSchema's integer-into-number
// collapse), then boolean. A string carries a type or format option from iq's
// inferred format. The format date-time becomes logicalType timestamp with no
// option. The format date becomes logicalType date with a JDK date pattern. A UUID
// stays logicalType string with the ODCS uuid format. A null-only or unknown node
// has no ODCS logical type, because ODCS expresses nullability by the absence of
// required, not by a type.
func (n *node) odcsLogicalType() (string, map[string]any) {
	switch {
	case n.kinds.has(kindMap):
		return odcsTypeMap, nil
	case n.kinds.has(kindObject):
		return odcsTypeObject, nil
	case n.kinds.has(kindArray):
		return odcsTypeArray, nil
	case n.kinds.has(kindString):
		switch n.fmt.resolve() {
		case "date-time":
			return odcsTypeTimestamp, nil
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

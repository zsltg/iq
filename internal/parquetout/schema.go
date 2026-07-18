// Package parquetout streams a normalized value sequence to Apache Parquet. It
// buffers the first sample of values, infers their structural shape with
// internal/shape, projects that shape's JSON Schema onto an Arrow schema, and
// writes one Parquet record batch per page with pqarrow. The core stays
// driver-agnostic: this package consumes only the closed normalized value set
// (nil, bool, int/*big.Int, float64, json.Number, string, []any, map[string]any)
// that crosses the query boundary, never a driver row.
//
// The Arrow dependency is contained here — internal/shape and the query core
// never import it. The schema is projected from shape.JSONSchema (the public
// lattice projection) rather than the shape's private node tree.
package parquetout

import (
	"fmt"
	"sort"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/zsltg/iq/internal/shape"
)

// jsonEnc pairs the arrow.json fallback type with its encoder plan. The storage
// is plain utf8 carrying byte-lossless canonical JSON; the column is marked as
// the arrow.json canonical extension through field metadata (see fieldMetadata),
// not through the registered Arrow extension type. arrow-go's registry, given
// the reserved ARROW:extension:name key, rewrites the column to a JSON Parquet
// logical type it then cannot read back (storage reads as binary, the extension
// demands utf8) — the registry resists, so the marker rides in iq's namespace
// while the utf8 storage round-trips cleanly.
func jsonEnc() (arrow.DataType, *enc) {
	// kind defaults to encJSON, the zero value of encKind.
	return arrow.BinaryTypes.String, &enc{}
}

// sampleBufferSize is how many leading values are buffered before the schema is
// inferred and the first page is written. It mirrors the schema-inference sample
// default (cmd schema/diff --sample), so the exported shape matches iq schema.
// It is a constant, not a flag, for v1: a wider sample yields a truer shape, but
// the trade-off is fixed until a real need surfaces.
const sampleBufferSize = 1000

// pageBatchSize is how many rows accumulate into one Arrow record batch before
// it is written. It reuses the move/scan page size so memory stays bounded to
// one page once the sample is drained.
const pageBatchSize = 500

// jsonExtName is the Arrow canonical-extension name a heterogeneous, null-only,
// or unknown column carries, recorded as the value of the metaExtName marker.
const jsonExtName = "arrow.json"

// Metadata keys attached to a column or struct field. Both live in iq's
// namespace: metaExtName deliberately avoids the reserved ARROW:extension:name
// key, which arrow-go's registry reconstructs into an unreadable JSON Parquet
// column.
const (
	metaPresence = "iq:presence"  // required|optional, parent-relative from shape.
	metaExtName  = "iq:extension" // arrow.json for the byte-lossless JSON fallback.
)

// encKind is how one position in the value tree is encoded into its Arrow
// builder. It mirrors the resolved Arrow type so the encoder can dispatch
// without re-inspecting the DataType at every append.
type encKind int

const (
	encJSON encKind = iota // utf8 storage, canonical JSON text (arrow.json fallback).
	encInt
	encFloat
	encBool
	encString
	encTimestamp // timestamp[ns, UTC], RFC3339Nano source text.
	encDate      // date32, YYYY-MM-DD source text.
	encStruct
	encList
	encMap // map<utf8, elem>.
)

// enc is the encoder plan for one value position: its kind, and for composites
// the child plans. afields holds the Arrow fields of a struct so the schema can
// be built and, at the root, flattened into columns.
type enc struct {
	kind    encKind
	afields []arrow.Field
	fields  []encField
	elem    *enc
}

// encField pairs a struct field's name with its encoder plan.
type encField struct {
	name string
	enc  *enc
}

// plan is the resolved write plan: the Arrow schema, the per-column encoders,
// and whether the whole value is a single "value" column (the root shape was not
// an object with fields) rather than one column per field.
type plan struct {
	schema      *arrow.Schema
	columns     []encField
	singleValue bool
}

// inferPlan reduces the sampled values to a shape, projects it to JSON Schema,
// and builds the Arrow write plan. An empty sample yields a single null-only
// arrow.json "value" column, so a zero-row export is still a valid Parquet file.
// It cannot fail: shape inference and the shape→Arrow projection are total.
func inferPlan(sample []any) *plan {
	items := make(map[string]any, len(sample))
	for i, v := range sample {
		items[fmt.Sprintf("%d", i)] = v
	}
	root := shape.Infer(items).JSONSchema("parquet")

	dt, e := typeFor(root)
	if e.kind == encStruct {
		return &plan{
			schema:  arrow.NewSchema(e.afields, nil),
			columns: e.fields,
		}
	}
	// A non-object (or heterogeneous) root has no columns to spread; the whole
	// value becomes one "value" column.
	f := arrow.Field{Name: "value", Type: dt, Nullable: true, Metadata: fieldMetadata("required", e.kind == encJSON)}
	return &plan{
		schema:      arrow.NewSchema([]arrow.Field{f}, nil),
		columns:     []encField{{name: "value", enc: e}},
		singleValue: true,
	}
}

// typeFor resolves one JSON Schema subschema to its Arrow data type and encoder
// plan. A position with more than one non-null type, no type, or only null
// collapses to the arrow.json fallback — byte-lossless canonical JSON in utf8
// storage, per the Arrow canonical-extensions spec.
func typeFor(sub map[string]any) (arrow.DataType, *enc) {
	names := nonNullTypes(sub["type"])
	if len(names) != 1 {
		return jsonEnc()
	}
	switch names[0] {
	case "integer":
		return arrow.PrimitiveTypes.Int64, &enc{kind: encInt}
	case "number":
		return arrow.PrimitiveTypes.Float64, &enc{kind: encFloat}
	case "boolean":
		return arrow.FixedWidthTypes.Boolean, &enc{kind: encBool}
	case "string":
		return stringType(sub)
	case "array":
		return arrayType(sub)
	case "object":
		return objectType(sub)
	default:
		return jsonEnc()
	}
}

// stringType maps a string subschema, promoting the shape's date-time and date
// formats to native temporal Arrow types; uuid stays utf8 for v1.
func stringType(sub map[string]any) (arrow.DataType, *enc) {
	switch format(sub) {
	case "date-time":
		return &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"}, &enc{kind: encTimestamp}
	case "date":
		return arrow.FixedWidthTypes.Date32, &enc{kind: encDate}
	default:
		return arrow.BinaryTypes.String, &enc{kind: encString}
	}
}

// arrayType maps an array subschema to a list of its element type. An array with
// no observed elements has no items schema; its element falls back to arrow.json.
func arrayType(sub map[string]any) (arrow.DataType, *enc) {
	items, ok := sub["items"].(map[string]any)
	if !ok {
		items = map[string]any{}
	}
	elemDT, elemEnc := typeFor(items)
	return arrow.ListOf(elemDT), &enc{kind: encList, elem: elemEnc}
}

// objectType maps an object subschema: an id-keyed map (additionalProperties)
// becomes map<utf8, elem>; a fixed record (properties) becomes a struct; an
// object with neither observed falls back to arrow.json.
func objectType(sub map[string]any) (arrow.DataType, *enc) {
	if ap, ok := sub["additionalProperties"].(map[string]any); ok {
		valDT, valEnc := typeFor(ap)
		return arrow.MapOf(arrow.BinaryTypes.String, valDT), &enc{kind: encMap, elem: valEnc}
	}
	// shape.JSONSchema only emits "properties" for an object with at least one
	// field, so a present map is never empty; !ok (no properties) is the only
	// no-field case and folds to the arrow.json fallback.
	props, ok := sub["properties"].(map[string]any)
	if !ok {
		return jsonEnc()
	}
	required := requiredSet(sub["required"])
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	afields := make([]arrow.Field, 0, len(names))
	fields := make([]encField, 0, len(names))
	for _, name := range names {
		child, _ := props[name].(map[string]any)
		fdt, fenc := typeFor(child)
		presence := "optional"
		if required[name] {
			presence = "required"
		}
		afields = append(afields, arrow.Field{
			Name:     name,
			Type:     fdt,
			Nullable: true,
			Metadata: fieldMetadata(presence, fenc.kind == encJSON),
		})
		fields = append(fields, encField{name: name, enc: fenc})
	}
	return arrow.StructOf(afields...), &enc{kind: encStruct, afields: afields, fields: fields}
}

// nonNullTypes returns the subschema's declared JSON Schema types with "null"
// removed: a JSON null rides in the Arrow validity bitmap, so integer-or-null is
// a nullable int64, not a heterogeneous column. A single remaining type maps to
// its Arrow type; zero or several remaining collapse to arrow.json.
func nonNullTypes(raw any) []string {
	var names []string
	switch t := raw.(type) {
	case string:
		names = []string{t}
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				names = append(names, s)
			}
		}
	}
	out := names[:0:0]
	for _, n := range names {
		if n != "null" {
			out = append(out, n)
		}
	}
	return out
}

// format returns a string subschema's format ("date-time"/"date"/"uuid"), or "".
func format(sub map[string]any) string {
	f, _ := sub["format"].(string)
	return f
}

// requiredSet turns a JSON Schema "required" array into a lookup set.
func requiredSet(raw any) map[string]bool {
	set := map[string]bool{}
	if arr, ok := raw.([]any); ok {
		for _, e := range arr {
			if s, ok := e.(string); ok {
				set[s] = true
			}
		}
	}
	return set
}

// fieldMetadata builds a field's Arrow metadata: its parent-relative presence,
// and, for an arrow.json column, the canonical-extension name so the fallback is
// self-describing. Keys are emitted sorted for a deterministic schema, and both
// survive the Parquet round-trip under WithStoreSchema.
func fieldMetadata(presence string, isJSON bool) arrow.Metadata {
	if !isJSON {
		return arrow.NewMetadata([]string{metaPresence}, []string{presence})
	}
	// Sorted: metaExtName ("iq:extension") precedes metaPresence ("iq:presence").
	return arrow.NewMetadata(
		[]string{metaExtName, metaPresence},
		[]string{jsonExtName, presence},
	)
}

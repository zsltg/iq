// Package neo4jenvelope names the reserved keys iq injects into a Neo4j node or
// relationship value alongside its real properties. The live Bolt driver
// (drivers/neo4j) and the offline APOC-JSON reader (drivers/file) share it, so both
// produce byte-identical envelope keys and neither imports the other. This keeps the
// file reader free of the Bolt driver dependency. A reserved key overwrites a real
// property of the same name.
package neo4jenvelope

const (
	// FieldID is a node's or relationship's identity. The live driver stores its
	// elementId; the APOC-JSON reader stores the export's numeric id.
	FieldID = "_id"
	// FieldLabels is a node's label list.
	FieldLabels = "_labels"
	// FieldType is a relationship's type.
	FieldType = "_type"
	// FieldStart is a relationship's start-node identity.
	FieldStart = "_start"
	// FieldEnd is a relationship's end-node identity.
	FieldEnd = "_end"
)

// reserved is the set of injected keys that are not real properties.
var reserved = map[string]struct{}{
	FieldID: {}, FieldLabels: {}, FieldType: {}, FieldStart: {}, FieldEnd: {},
}

// IsReserved reports whether key is an injected envelope key rather than a real
// property, so a writer strips it before persisting a read-back value.
func IsReserved(key string) bool {
	_, ok := reserved[key]
	return ok
}

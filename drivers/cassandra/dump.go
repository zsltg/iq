package cassandra

import (
	"fmt"
	"strings"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
)

// BindString parses a single string field (a cell from a cqlsh COPY dump, whose values
// are all text) into the Go value gocql yields for a live column of the given CQL type,
// so an offline dump reader can feed Normalize and KeyOf the exact typed values a live
// scan produces. It is the exported façade over bindKeyValue, scoped to the scalar CQL
// types a COPY dump carries; a type without a dedicated parse binds as the raw string.
func BindString(t gocql.Type, s string) (any, error) {
	return bindKeyValue(t, s)
}

// ParseColumnTypes parses a ?types= hint into a column-name → CQL type map, so an
// offline dump reader re-types cqlsh COPY output that a live scan would receive already
// typed. The hint is a comma-separated list of "column=cqltype" entries, e.g.
// "id=uuid,created=timestamp,count=int". Only scalar CQL types are supported (a COPY
// dump of a collection column is out of scope); an unknown or non-scalar type is an
// error so a typo or an unsupported column fails fast. A column absent from the hint is
// read as text by the reader, so only non-text columns need listing.
func ParseColumnTypes(hint string) (map[string]gocql.Type, error) {
	out := map[string]gocql.Type{}
	if strings.TrimSpace(hint) == "" {
		return out, nil
	}
	for entry := range strings.SplitSeq(hint, ",") {
		col, typ, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("cassandra: malformed ?types= entry %q: want column=cqltype", entry)
		}
		col = strings.TrimSpace(col)
		if col == "" {
			return nil, fmt.Errorf("cassandra: empty column name in ?types= entry %q", entry)
		}
		ct, err := parseCQLType(strings.TrimSpace(typ))
		if err != nil {
			return nil, err
		}
		out[col] = ct
	}
	return out, nil
}

// parseCQLType maps a scalar CQL type name to its gocql type id, the offline analogue of
// the type a live column carries. Only the scalar types bindKeyValue handles are
// accepted; collections and other complex types are rejected rather than silently
// mistyped.
func parseCQLType(name string) (gocql.Type, error) {
	switch strings.ToLower(name) {
	case "text", "varchar":
		return gocql.TypeText, nil
	case "ascii":
		return gocql.TypeAscii, nil
	case "int":
		return gocql.TypeInt, nil
	case "smallint":
		return gocql.TypeSmallInt, nil
	case "tinyint":
		return gocql.TypeTinyInt, nil
	case "bigint":
		return gocql.TypeBigInt, nil
	case "counter":
		return gocql.TypeCounter, nil
	case "varint":
		return gocql.TypeVarint, nil
	case "float":
		return gocql.TypeFloat, nil
	case "double":
		return gocql.TypeDouble, nil
	case "decimal":
		return gocql.TypeDecimal, nil
	case "boolean", "bool":
		return gocql.TypeBoolean, nil
	case "uuid":
		return gocql.TypeUUID, nil
	case "timeuuid":
		return gocql.TypeTimeUUID, nil
	case "timestamp":
		return gocql.TypeTimestamp, nil
	case "blob":
		return gocql.TypeBlob, nil
	case "inet":
		return gocql.TypeInet, nil
	default:
		return gocql.TypeCustom, fmt.Errorf("cassandra: unsupported ?types= column type %q: want a scalar CQL type (text, int, uuid, timestamp, …)", name)
	}
}

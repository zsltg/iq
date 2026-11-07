package neo4j

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/zsltg/iq/internal/neo4jenvelope"
	"github.com/zsltg/iq/internal/query"
)

// recordProps derives the property map and merge-key value for one write record. The
// value must be a JSON object (a node's properties). iq's reserved envelope keys are
// stripped. Neo4j cannot store a null property or a map property, so a null, a map,
// a list with a null, a map or a list inside, or a list of mixed types is refused. When the record has a key, it wins as the merge key. If the
// value's own key property has the same string form (the form the read path gives a
// key), the typed value stays, so 5 stays a number. Otherwise the record key string
// is the merge key. A keyless record merges on the value's key property, which must
// be present. The merge key is always written onto the node.
func recordProps(rec query.Record, key string) (map[string]any, any, error) {
	obj, ok := rec.Value.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("neo4j: node value for key %q must be a JSON object, got %T", rec.Key, rec.Value)
	}
	props := make(map[string]any, len(obj))
	for k, v := range obj {
		if neo4jenvelope.IsReserved(k) {
			continue
		}
		if err := checkProp(v); err != nil {
			return nil, nil, fmt.Errorf("neo4j: key %q: property %q %w", rec.Key, k, err)
		}
		props[k] = v
	}
	keyVal, err := mergeKey(rec.Key, key, props)
	if err != nil {
		return nil, nil, err
	}
	props[key] = keyVal
	return props, keyVal, nil
}

// checkProp refuses a property value that Neo4j cannot store exactly. Neo4j drops a
// null property on write, so it would read back as missing. A property holds a scalar
// or a list of scalars of one type, so a map is refused, and checkList refuses a bad
// list. The error text follows the property name, which the caller adds.
func checkProp(v any) error {
	if v == nil {
		return errors.New("is null, and Neo4j cannot store a null property")
	}
	if _, isMap := v.(map[string]any); isMap {
		return errors.New("is a map, and Neo4j cannot store a map as a property")
	}
	// A value that is not a list gives a nil list, which holds no bad element.
	list, _ := v.([]any)
	return checkList(list)
}

// checkList refuses a list that Neo4j cannot store as a property: a list with a null,
// a map or a list inside, or a list of more than one scalar type. An empty list is
// stored and read back as an empty list, so it is kept.
func checkList(list []any) error {
	if slices.ContainsFunc(list, func(e any) bool { return e == nil }) {
		return errors.New("holds a null, and Neo4j cannot store a null in a list")
	}
	if slices.ContainsFunc(list, isContainer) {
		return errors.New("holds a map or a list, and Neo4j cannot store a nested value in a list")
	}
	if !oneScalarKind(list) {
		return errors.New("holds more than one type, and Neo4j stores a list of one type only")
	}
	return nil
}

// isContainer reports whether v is a map or a list.
func isContainer(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// scalarKind is the Neo4j storage type of a scalar list element. Neo4j stores a
// list of integers and a list of floats as different array types, and it turns an
// integer into a float when one list holds both, which is not exact for a large
// integer. So an integer and a float are two kinds.
type scalarKind int

const (
	kindOther scalarKind = iota // a type that the JSON decode does not give; the server decides.
	kindString
	kindBool
	kindInteger
	kindFloat
)

// kindOf returns the scalarKind of a decoded value. A json.Number is an integer
// when its text has no '.', 'e' or 'E', which is how iq decodes a number.
func kindOf(v any) scalarKind {
	switch x := v.(type) {
	case string:
		return kindString
	case bool:
		return kindBool
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, *big.Int:
		return kindInteger
	case float32, float64:
		return kindFloat
	case json.Number:
		if strings.ContainsAny(x.String(), ".eE") {
			return kindFloat
		}
		return kindInteger
	}
	return kindOther
}

// oneScalarKind reports whether every element of the list has the same scalarKind.
// An element of kindOther is not compared.
func oneScalarKind(list []any) bool {
	first := kindOther
	for _, e := range list {
		k := kindOf(e)
		if k == kindOther {
			continue
		}
		if first == kindOther {
			first = k
		}
		if k != first {
			return false
		}
	}
	return true
}

// mergeKey picks the merge-key value. The record key wins. The value's key property
// is kept only when its string form equals the record key.
func mergeKey(recKey, key string, props map[string]any) (any, error) {
	own, has := props[key]
	if recKey == "" {
		if !has {
			return nil, fmt.Errorf("neo4j: a keyless record needs a %q property in its value", key)
		}
		return own, nil
	}
	// A missing key property gives "", which never equals a non-empty record key.
	if keyString(own) == recKey {
		return own, nil
	}
	return recKey, nil
}

// keyString renders a typed key property the way the read path renders a key
// (Cypher toString), so mergeKey can keep its type when it equals the record key.
// It returns "" for any other value, which never equals a non-empty record key. A
// string needs no case: when it equals the record key, the record key is the same
// string.
func keyString(v any) string {
	switch x := v.(type) {
	case float64:
		return floatKeyString(x)
	case bool, int, int32, int64:
		return fmt.Sprint(x)
	}
	return ""
}

// floatKeyString renders a float the way Cypher toString does for a whole or a
// short fractional float: a whole float keeps a ".0", so 5.0 becomes "5.0".
func floatKeyString(f float64) string {
	s := fmt.Sprint(f)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

package neo4j

import (
	"fmt"
	"slices"
	"strings"

	"github.com/zsltg/iq/internal/neo4jenvelope"
	"github.com/zsltg/iq/internal/query"
)

// recordProps derives the property map and merge-key value for one write record. The
// value must be a JSON object (a node's properties). iq's reserved envelope keys are
// stripped. Neo4j cannot store a null property, so a null property, or a null inside a
// list, is refused. When the record has a key, it wins as the merge key. If the
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
		if err := checkProp(rec.Key, k, v); err != nil {
			return nil, nil, err
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

// checkProp refuses a null property and a null inside a list, because Neo4j drops
// a null property on write, so it would read back as missing.
func checkProp(recKey, name string, v any) error {
	if v == nil {
		return fmt.Errorf("neo4j: key %q: property %q is null, and Neo4j cannot store a null property", recKey, name)
	}
	// A value that is not a list gives a nil list, which holds no null.
	list, _ := v.([]any)
	if slices.ContainsFunc(list, func(e any) bool { return e == nil }) {
		return fmt.Errorf("neo4j: key %q: property %q holds a null, and Neo4j cannot store a null in a list", recKey, name)
	}
	return nil
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

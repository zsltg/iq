package mongo

import (
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/predicate"
)

// toFilter translates a neutral predicate into a MongoDB find filter. An Eq
// becomes a field equality on the dotted path; And and Or become $and and $or.
// A nil predicate (nothing to push) becomes an empty filter that matches every
// document, so the caller falls back to a full scan.
func toFilter(n predicate.Node) bson.M {
	switch t := n.(type) {
	case nil:
		return bson.M{}
	case predicate.Eq:
		return bson.M{strings.Join(t.Path, "."): t.Value}
	case predicate.Cmp:
		return cmpFilter(t)
	case predicate.Regex:
		re := bson.M{"$regex": t.Pattern}
		if t.Flags != "" {
			re["$options"] = t.Flags
		}
		return bson.M{strings.Join(t.Path, "."): re}
	case predicate.Exists:
		return bson.M{strings.Join(t.Path, "."): bson.M{"$exists": true}}
	case predicate.Size:
		return sizeFilter(t)
	case predicate.ElemMatch:
		path := strings.Join(t.Path, ".")
		// $elemMatch matches an array element satisfying the condition. jq's any
		// also iterates an object's values, so an object at the path is a possible
		// match too; the client-side re-run confirms it.
		return bson.M{"$or": bson.A{
			bson.M{path: bson.M{"$elemMatch": toFilter(t.Cond)}},
			bson.M{path: bson.M{"$type": "object"}},
		}}
	case predicate.Ne:
		// jq's != excludes only the scalar value; an array is never equal to a
		// scalar in jq, so it must still match even though Mongo's $ne would
		// exclude an array that contains the value.
		p := strings.Join(t.Path, ".")
		return bson.M{"$or": bson.A{
			bson.M{p: bson.M{"$ne": t.Value}},
			bson.M{p: bson.M{"$type": "array"}},
		}}
	case predicate.NotExists:
		return bson.M{strings.Join(t.Path, "."): bson.M{"$exists": false}}
	case predicate.NoneMatch:
		// No array element satisfies the (exact) condition.
		return bson.M{strings.Join(t.Path, "."): bson.M{"$not": bson.M{"$elemMatch": exactFilter(t.Cond)}}}
	case predicate.And:
		return bson.M{"$and": toFilters(t)}
	case predicate.Or:
		// An or of equalities on one field is exactly a membership test, which
		// Mongo expresses (and indexes) more directly as $in.
		if path, values, ok := sameFieldEqs(t); ok {
			return bson.M{path: bson.M{"$in": values}}
		}
		return bson.M{"$or": toFilters(t)}
	default:
		// An unknown node type would be a programming error in the compiler;
		// fall back to matching everything so the client-side jq still runs.
		return bson.M{}
	}
}

// sameFieldEqs reports whether every branch of an or is an equality on the same
// field, and if so returns that field's dotted path and the values, so the or can
// collapse to a single $in.
func sameFieldEqs(or predicate.Or) (string, bson.A, bool) {
	if len(or) < 2 {
		return "", nil, false
	}
	var path string
	values := make(bson.A, 0, len(or))
	for i, n := range or {
		eq, ok := n.(predicate.Eq)
		if !ok {
			return "", nil, false
		}
		p := strings.Join(eq.Path, ".")
		if i == 0 {
			path = p
		} else if p != path {
			return "", nil, false
		}
		values = append(values, eq.Value)
	}
	return path, values, true
}

// exactFilter renders an equality condition exactly (not as the array-containment
// superset Eq uses), so it can be safely negated. `{field: value}` alone would
// also match an array element containing value; the extra guard excludes arrays,
// leaving exactly "field is the scalar value".
func exactFilter(n predicate.Node) bson.M {
	switch t := n.(type) {
	case predicate.Eq:
		p := strings.Join(t.Path, ".")
		return bson.M{"$and": bson.A{
			bson.M{p: t.Value},
			bson.M{p: bson.M{"$not": bson.M{"$type": "array"}}},
		}}
	case predicate.And:
		return bson.M{"$and": exactFilters(t)}
	case predicate.Or:
		return bson.M{"$or": exactFilters(t)}
	default:
		// Unreachable: NoneMatch is only built over exact conditions.
		return bson.M{}
	}
}

// exactFilters renders each child exactly, preserving order.
func exactFilters(nodes []predicate.Node) bson.A {
	out := make(bson.A, len(nodes))
	for i, n := range nodes {
		out[i] = exactFilter(n)
	}
	return out
}

// toFilters translates each child node, preserving order.
func toFilters(nodes []predicate.Node) bson.A {
	out := make(bson.A, len(nodes))
	for i, n := range nodes {
		out[i] = toFilter(n)
	}
	return out
}

// jqTypes lists the BSON $type names in jq's total order, so index i is the type
// jq ranks at position i (null lowest, object highest). "number" and "bool" are
// the Mongo aliases covering every numeric / boolean BSON type.
var jqTypes = []string{"null", "bool", "number", "string", "array", "object"}

// mongoOp maps a range operator to its Mongo query operator.
var mongoOp = map[predicate.Op]string{
	predicate.Gt: "$gt",
	predicate.Ge: "$gte",
	predicate.Lt: "$lt",
	predicate.Le: "$lte",
}

// cmpFilter translates a range comparison into a conservative superset that
// reproduces jq's cross-type ordering. jq compares across types (null < bool <
// number < string < array < object), so `field > value` also matches every value
// whose type ranks strictly above the literal's, and `field < value` also matches
// every type strictly below plus a missing field (jq reads a missing field as
// null, the lowest value). The same-type bound uses the native operator; the
// cross-type parts are added with $type. Extra matches are harmless because the
// caller re-runs the full jq; missing one would be a bug, which is why the type
// clauses are exhaustive.
func cmpFilter(c predicate.Cmp) bson.M {
	path := strings.Join(c.Path, ".")
	clauses := bson.A{bson.M{path: bson.M{mongoOp[c.Op]: c.Value}}}

	// The literal is always a number (rank 2) or string (rank 3), so the type
	// lists below are always non-empty — no guard needed.
	rank := valueRank(c.Value)
	if c.Op == predicate.Gt || c.Op == predicate.Ge {
		clauses = append(clauses, bson.M{path: bson.M{"$type": asA(jqTypes[rank+1:])}})
	} else {
		clauses = append(clauses, bson.M{path: bson.M{"$type": asA(jqTypes[:rank])}})
		// A missing field is null in jq, so it is below any number or string.
		clauses = append(clauses, bson.M{path: bson.M{"$exists": false}})
	}
	return bson.M{"$or": clauses}
}

// sizeFilter translates a jq `length == n` test. jq's length is polymorphic, so
// $size (array element count) alone would be a subset — it would miss a string of
// n characters, an object of n keys, or a number whose absolute value is n. The
// filter therefore matches arrays of exactly n via $size and, conservatively,
// every string/object/number (their length matches are stripped by the
// client-side re-run). Length 0 additionally matches null and a missing field,
// which jq reads as length 0.
func sizeFilter(s predicate.Size) bson.M {
	path := strings.Join(s.Path, ".")
	types := bson.A{"string", "object", "number"}
	clauses := bson.A{
		bson.M{path: bson.M{"$size": s.N}},
		bson.M{path: bson.M{"$type": types}},
	}
	if s.N == 0 {
		clauses = append(
			clauses,
			bson.M{path: bson.M{"$type": "null"}},
			bson.M{path: bson.M{"$exists": false}},
		)
	}
	return bson.M{"$or": clauses}
}

// valueRank returns the jq type rank of a range literal: 2 for a number, 3 for a
// string (the only two kinds the compiler emits).
func valueRank(v any) int {
	if _, ok := v.(string); ok {
		return 3
	}
	return 2
}

// asA widens a string slice to a bson.A for a $type list.
func asA(types []string) bson.A {
	out := make(bson.A, len(types))
	for i, t := range types {
		out[i] = t
	}
	return out
}

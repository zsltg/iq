package mongo

import (
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/pushdown"
)

// toFilter translates a neutral predicate into a MongoDB find filter on the
// dotted path. It pushes every node type: Eq, Cmp, Regex, Exists, Size,
// ElemMatch, Ne, NotExists, NoneMatch, And, and Or. A nil predicate (nothing to
// push) becomes an empty filter that matches every document, so the caller
// falls back to a full scan.
func toFilter(n predicate.Node, mode numfmt.DecimalMode) bson.M {
	switch t := n.(type) {
	case nil:
		return bson.M{}
	case predicate.Eq:
		return widenDecimal(t, t.Path, bson.M{strings.Join(t.Path, "."): t.Value}, mode)
	case predicate.Cmp:
		return widenDecimal(t, t.Path, cmpFilter(t), mode)
	case predicate.Regex:
		re := bson.M{"$regex": t.Pattern}
		if opts := mongoOptions(t.Flags); opts != "" {
			re["$options"] = opts
		}
		return widenDecimal(t, t.Path, bson.M{strings.Join(t.Path, "."): re}, mode)
	case predicate.Exists:
		return bson.M{strings.Join(t.Path, "."): bson.M{"$exists": true}}
	case predicate.Size:
		return sizeFilter(t)
	case predicate.ElemMatch:
		path := strings.Join(t.Path, ".")
		// $elemMatch matches an array element that satisfies the condition. jq's
		// any also iterates the values of an object. So an object at the path is a
		// possible match too. The client-side re-run confirms it.
		return bson.M{"$or": bson.A{
			bson.M{path: bson.M{"$elemMatch": toFilter(t.Cond, mode)}},
			bson.M{path: bson.M{"$type": "object"}},
		}}
	case predicate.Ne:
		// jq's != excludes only the scalar value. In jq, an array is never
		// equal to a scalar, so an array must still match. Mongo's $ne
		// differs: it excludes an array that contains the value.
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
		return bson.M{"$and": toFilters(t, mode)}
	case predicate.Or:
		// An or of equalities on one field is exactly a membership test, which
		// Mongo expresses (and indexes) more directly as $in.
		if path, values, ok := sameFieldEqs(t, mode); ok {
			return bson.M{path: bson.M{"$in": values}}
		}
		return bson.M{"$or": toFilters(t, mode)}
	default:
		// An unknown node type is a programming error in the compiler. Fall
		// back to matching everything, so the client-side jq still runs.
		return bson.M{}
	}
}

// widenDecimal returns leaf, or leaf widened to also match a Decimal128 on the
// path, if the decimal mode decodes a Decimal128 to a string that changes the verdict
// of the node. The server compares a Decimal128 as a number. jq compares the decoded
// string. So the server could drop a document that jq keeps. A double decodes to a
// float64 in every mode, so only the decimal type needs the widening. The widened
// leaf is a superset, not an exact test. See pushdown.HidesDecimalNumber.
func widenDecimal(n predicate.Node, path []string, leaf bson.M, mode numfmt.DecimalMode) bson.M {
	if !pushdown.HidesDecimalNumber(n, mode) {
		return leaf
	}
	return bson.M{"$or": bson.A{
		leaf,
		bson.M{strings.Join(path, "."): bson.M{"$type": "decimal"}},
	}}
}

// mongoOptions translates the jq flags of a neutral Regex into the $options
// spelling (PCRE) of MongoDB. The jq flag i (case-insensitive) is the i of PCRE.
// The jq flag m (dotall) is the s of PCRE, not the m of PCRE. The m of PCRE is
// line-anchor multiline. Mapping the jq m to it leaves . not matching a newline,
// which narrows the match against jq. The pushdown gate admits only i and m.
func mongoOptions(flags string) string {
	var b strings.Builder
	for _, f := range flags {
		switch f {
		case 'i':
			b.WriteByte('i')
		case 'm':
			b.WriteByte('s')
		}
	}
	return b.String()
}

// sameFieldEqs reports whether every branch of an or is an equality on the same
// field. If so, it returns the dotted path of that field and the values, so the
// or can collapse to a single $in. An equality that widenDecimal widens stops the
// collapse.
func sameFieldEqs(or predicate.Or, mode numfmt.DecimalMode) (string, bson.A, bool) {
	if len(or) < 2 {
		return "", nil, false
	}
	var path string
	values := make(bson.A, 0, len(or))
	for i, n := range or {
		eq, ok := n.(predicate.Eq)
		// A widened equality cannot join the $in, which has no room for the $type clause.
		if !ok || pushdown.HidesDecimalNumber(eq, mode) {
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
// superset that Eq uses), so it can be safely negated. `{field: value}` alone
// also matches an array element that contains value. The extra guard excludes
// arrays, and leaves exactly "field is the scalar value".
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
func toFilters(nodes []predicate.Node, mode numfmt.DecimalMode) bson.A {
	out := make(bson.A, len(nodes))
	for i, n := range nodes {
		out[i] = toFilter(n, mode)
	}
	return out
}

// jqTypes lists the BSON $type names in the total order of jq. So index i is the
// type that jq ranks at position i (null lowest, object highest). The names
// "number" and "bool" are the Mongo aliases. They cover every numeric and
// boolean BSON type.
var jqTypes = []string{"null", "bool", "number", "string", "array", "object"}

// mongoOp maps a range operator to its Mongo query operator.
var mongoOp = map[predicate.Op]string{
	predicate.Gt: "$gt",
	predicate.Ge: "$gte",
	predicate.Lt: "$lt",
	predicate.Le: "$lte",
}

// cmpFilter translates a range comparison into a superset that follows jq's
// cross-type ordering (null < bool < number < string < array < object). So
// `field > value` also matches every value of a type that ranks above the
// literal's. `field < value` also matches every lower type, plus a missing field
// (jq reads it as null, the lowest value). The same-type bound uses the native
// operator. The cross-type parts use $type, and they must be exhaustive, because a
// missed match is a bug.
func cmpFilter(c predicate.Cmp) bson.M {
	path := strings.Join(c.Path, ".")
	clauses := bson.A{bson.M{path: bson.M{mongoOp[c.Op]: c.Value}}}

	// The literal is always a number (rank 2) or string (rank 3), so the type
	// lists below are always non-empty. No guard is
	// needed.
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

// sizeFilter translates a jq `length == n` test. The jq length is polymorphic.
// So $size (array element count) alone misses a string of n characters, an object
// of n keys, or a number with absolute value n. The filter matches arrays of
// exactly n via $size, plus every string, object, and number. Length 0 also
// matches null and a missing field, which jq reads as length 0.
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

package mongo

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
)

// TestToFilter runs in number mode, so a number equality and a number comparison
// carry the Decimal128 widening. The dec helper adds it to the expected filter.
func TestToFilter(t *testing.T) {
	tests := []struct {
		name string
		pred predicate.Node
		want bson.M
	}{
		{"nil matches everything", nil, bson.M{}},
		{"equality", predicate.Eq{Path: []string{"author"}, Value: "x"}, bson.M{"author": "x"}},
		{"nested path is dotted", predicate.Eq{Path: []string{"a", "b"}, Value: 1.0}, dec("a.b", bson.M{"a.b": 1.0})},
		{
			"greater than a number: adds higher jq types",
			predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2015.0},
			dec("year", bson.M{"$or": bson.A{
				bson.M{"year": bson.M{"$gt": 2015.0}},
				bson.M{"year": bson.M{"$type": bson.A{"string", "array", "object"}}},
			}}),
		},
		{
			"less than a number: adds lower jq types and missing",
			predicate.Cmp{Path: []string{"year"}, Op: predicate.Lt, Value: 2015.0},
			dec("year", bson.M{"$or": bson.A{
				bson.M{"year": bson.M{"$lt": 2015.0}},
				bson.M{"year": bson.M{"$type": bson.A{"null", "bool"}}},
				bson.M{"year": bson.M{"$exists": false}},
			}}),
		},
		{
			"greater or equal a string: adds array and object",
			predicate.Cmp{Path: []string{"name"}, Op: predicate.Ge, Value: "m"},
			bson.M{"$or": bson.A{
				bson.M{"name": bson.M{"$gte": "m"}},
				bson.M{"name": bson.M{"$type": bson.A{"array", "object"}}},
			}},
		},
		{
			"less or equal a string: adds null, bool, number and missing",
			predicate.Cmp{Path: []string{"name"}, Op: predicate.Le, Value: "m"},
			bson.M{"$or": bson.A{
				bson.M{"name": bson.M{"$lte": "m"}},
				bson.M{"name": bson.M{"$type": bson.A{"null", "bool", "number"}}},
				bson.M{"name": bson.M{"$exists": false}},
			}},
		},
		{
			"and",
			predicate.And{
				predicate.Eq{Path: []string{"a"}, Value: 1.0},
				predicate.Eq{Path: []string{"b"}, Value: "x"},
			},
			bson.M{"$and": bson.A{dec("a", bson.M{"a": 1.0}), bson.M{"b": "x"}}},
		},
		{
			"regex without flags",
			predicate.Regex{Path: []string{"name"}, Pattern: "^A"},
			bson.M{"name": bson.M{"$regex": "^A"}},
		},
		{
			"regex with flags",
			predicate.Regex{Path: []string{"name"}, Pattern: "^a", Flags: "i"},
			bson.M{"name": bson.M{"$regex": "^a", "$options": "i"}},
		},
		{
			// jq's m is dotall, which is the s of PCRE (not the m of PCRE). The
			// translation
			// keeps . matching a newline server-side, so the push stays a superset.
			"regex with dotall m flag maps to option s",
			predicate.Regex{Path: []string{"name"}, Pattern: "a.c", Flags: "m"},
			bson.M{"name": bson.M{"$regex": "a.c", "$options": "s"}},
		},
		{
			"regex with combined im flags maps to is",
			predicate.Regex{Path: []string{"name"}, Pattern: "a.c", Flags: "im"},
			bson.M{"name": bson.M{"$regex": "a.c", "$options": "is"}},
		},
		{
			"exists",
			predicate.Exists{Path: []string{"meta", "isbn"}},
			bson.M{"meta.isbn": bson.M{"$exists": true}},
		},
		{
			"size of a positive length",
			predicate.Size{Path: []string{"tags"}, N: 3},
			bson.M{"$or": bson.A{
				bson.M{"tags": bson.M{"$size": 3}},
				bson.M{"tags": bson.M{"$type": bson.A{"string", "object", "number"}}},
			}},
		},
		{
			"size of zero also matches null and missing",
			predicate.Size{Path: []string{"tags"}, N: 0},
			bson.M{"$or": bson.A{
				bson.M{"tags": bson.M{"$size": 0}},
				bson.M{"tags": bson.M{"$type": bson.A{"string", "object", "number"}}},
				bson.M{"tags": bson.M{"$type": "null"}},
				bson.M{"tags": bson.M{"$exists": false}},
			}},
		},
		{
			"inequality keeps arrays (they are never a scalar in jq)",
			predicate.Ne{Path: []string{"status"}, Value: "x"},
			bson.M{"$or": bson.A{
				bson.M{"status": bson.M{"$ne": "x"}},
				bson.M{"status": bson.M{"$type": "array"}},
			}},
		},
		{
			"not exists",
			predicate.NotExists{Path: []string{"opt"}},
			bson.M{"opt": bson.M{"$exists": false}},
		},
		{
			"none match uses an exact element equality",
			predicate.NoneMatch{Path: []string{"items"}, Cond: predicate.Eq{Path: []string{"p"}, Value: 6.0}},
			bson.M{"items": bson.M{"$not": bson.M{"$elemMatch": bson.M{"$and": bson.A{
				bson.M{"p": 6.0},
				bson.M{"p": bson.M{"$not": bson.M{"$type": "array"}}},
			}}}}},
		},
		{
			"elemMatch also matches an object field",
			predicate.ElemMatch{Path: []string{"items"}, Cond: predicate.Eq{Path: []string{"p"}, Value: 6.0}},
			bson.M{"$or": bson.A{
				bson.M{"items": bson.M{"$elemMatch": dec("p", bson.M{"p": 6.0})}},
				bson.M{"items": bson.M{"$type": "object"}},
			}},
		},
		{
			"or on different fields stays $or",
			predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: 1.0},
				predicate.Eq{Path: []string{"b"}, Value: 2.0},
			},
			bson.M{"$or": bson.A{dec("a", bson.M{"a": 1.0}), dec("b", bson.M{"b": 2.0})}},
		},
		{
			"or of two equalities on one field collapses to $in",
			predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Eq{Path: []string{"a"}, Value: "y"},
			},
			bson.M{"a": bson.M{"$in": bson.A{"x", "y"}}},
		},
		{
			// A single-branch or is not a membership test: the $in collapse needs at
			// least two equalities, so one branch stays an $or.
			"or of one equality stays $or",
			predicate.Or{predicate.Eq{Path: []string{"a"}, Value: 1.0}},
			bson.M{"$or": bson.A{dec("a", bson.M{"a": 1.0})}},
		},
		{
			// Neither branch is an equality, so the $in collapse must reject the whole
			// or. It must not read a zero-value equality out of the first non-Eq
			// branch.
			"or of two ranges on one field stays $or",
			predicate.Or{
				predicate.Cmp{Path: []string{"a"}, Op: predicate.Gt, Value: 5.0},
				predicate.Cmp{Path: []string{"a"}, Op: predicate.Lt, Value: 1.0},
			},
			bson.M{"$or": bson.A{
				dec("a", bson.M{"$or": bson.A{
					bson.M{"a": bson.M{"$gt": 5.0}},
					bson.M{"a": bson.M{"$type": bson.A{"string", "array", "object"}}},
				}}),
				dec("a", bson.M{"$or": bson.A{
					bson.M{"a": bson.M{"$lt": 1.0}},
					bson.M{"a": bson.M{"$type": bson.A{"null", "bool"}}},
					bson.M{"a": bson.M{"$exists": false}},
				}}),
			}},
		},
		{
			"or mixing equality and range stays $or",
			predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: 1.0},
				predicate.Cmp{Path: []string{"a"}, Op: predicate.Gt, Value: 5.0},
			},
			bson.M{"$or": bson.A{
				dec("a", bson.M{"a": 1.0}),
				dec("a", bson.M{"$or": bson.A{
					bson.M{"a": bson.M{"$gt": 5.0}},
					bson.M{"a": bson.M{"$type": bson.A{"string", "array", "object"}}},
				}}),
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, toFilter(tt.pred, numfmt.DecimalNumber))
		})
	}
}

// dec returns leaf widened to also match a Decimal128 at the top-level field path.
func dec(path string, leaf bson.M) bson.M {
	return bson.M{"$or": bson.A{leaf, bson.M{path: bson.M{"$type": "decimal"}}}}
}

// TestToFilterWidensLeavesThatHideDecimals pins the filter per decimal mode. A
// Decimal128 decodes to a jq string in auto and string mode. The server compares it
// as a number. So the filter must also return the Decimal128 values for each leaf
// that the string changes.
func TestToFilterWidensLeavesThatHideDecimals(t *testing.T) {
	price := []string{"price"}
	isDecimal := bson.M{"price": bson.M{"$type": "decimal"}}
	eq := predicate.Eq{Path: price, Value: "9.99"}
	gt := predicate.Cmp{Path: price, Op: predicate.Gt, Value: 1000.0}
	regex := predicate.Regex{Path: price, Pattern: "^9"}
	gtFilter := bson.M{"$or": bson.A{
		bson.M{"price": bson.M{"$gt": 1000.0}},
		bson.M{"price": bson.M{"$type": bson.A{"string", "array", "object"}}},
	}}
	tests := []struct {
		name string
		pred predicate.Node
		mode numfmt.DecimalMode
		want bson.M
	}{
		{"eq number text, auto", eq, numfmt.DecimalAuto, bson.M{"$or": bson.A{bson.M{"price": "9.99"}, isDecimal}}},
		{"eq number text, string", eq, numfmt.DecimalString, bson.M{"$or": bson.A{bson.M{"price": "9.99"}, isDecimal}}},
		{"eq number text, number", eq, numfmt.DecimalNumber, bson.M{"price": "9.99"}},
		{"eq plain text, auto", predicate.Eq{Path: price, Value: "apple"}, numfmt.DecimalAuto, bson.M{"price": "apple"}},
		{"eq number, auto", predicate.Eq{Path: price, Value: 9.99}, numfmt.DecimalAuto, bson.M{"price": 9.99}},
		{"gt, auto", gt, numfmt.DecimalAuto, bson.M{"$or": bson.A{gtFilter, isDecimal}}},
		{"gt, string", gt, numfmt.DecimalString, bson.M{"$or": bson.A{gtFilter, isDecimal}}},
		{"gt, number", gt, numfmt.DecimalNumber, bson.M{"$or": bson.A{gtFilter, isDecimal}}},
		{"regex, auto", regex, numfmt.DecimalAuto, bson.M{"$or": bson.A{bson.M{"price": bson.M{"$regex": "^9"}}, isDecimal}}},
		{"regex, number", regex, numfmt.DecimalNumber, bson.M{"price": bson.M{"$regex": "^9"}}},
		{
			"lt stays a superset, auto",
			predicate.Cmp{Path: price, Op: predicate.Lt, Value: 5.0},
			numfmt.DecimalAuto,
			bson.M{"$or": bson.A{
				bson.M{"price": bson.M{"$lt": 5.0}},
				bson.M{"price": bson.M{"$type": bson.A{"null", "bool"}}},
				bson.M{"price": bson.M{"$exists": false}},
			}},
		},
		{
			// Two equalities on one field would collapse to $in, which cannot hold
			// the $type widening. So each branch stays its own leaf.
			"or of number texts does not collapse to $in, auto",
			predicate.Or{eq, predicate.Eq{Path: price, Value: "1.5"}},
			numfmt.DecimalAuto,
			bson.M{"$or": bson.A{
				bson.M{"$or": bson.A{bson.M{"price": "9.99"}, isDecimal}},
				bson.M{"$or": bson.A{bson.M{"price": "1.5"}, isDecimal}},
			}},
		},
		{
			"or of number texts collapses to $in, number",
			predicate.Or{eq, predicate.Eq{Path: price, Value: "1.5"}},
			numfmt.DecimalNumber,
			bson.M{"price": bson.M{"$in": bson.A{"9.99", "1.5"}}},
		},
		{
			"and widens only the affected leaf, auto",
			predicate.And{eq, predicate.Eq{Path: []string{"name"}, Value: "ada"}},
			numfmt.DecimalAuto,
			bson.M{"$and": bson.A{
				bson.M{"$or": bson.A{bson.M{"price": "9.99"}, isDecimal}},
				bson.M{"name": "ada"},
			}},
		},
		{"eq number, number", predicate.Eq{Path: price, Value: 9.99}, numfmt.DecimalNumber, bson.M{"$or": bson.A{bson.M{"price": 9.99}, isDecimal}}},
		{
			"ge number, number",
			predicate.Cmp{Path: price, Op: predicate.Ge, Value: 9.99},
			numfmt.DecimalNumber,
			bson.M{"$or": bson.A{
				bson.M{"$or": bson.A{
					bson.M{"price": bson.M{"$gte": 9.99}},
					bson.M{"price": bson.M{"$type": bson.A{"string", "array", "object"}}},
				}},
				isDecimal,
			}},
		},
		{
			"lt number, number",
			predicate.Cmp{Path: price, Op: predicate.Lt, Value: 10.0},
			numfmt.DecimalNumber,
			bson.M{"$or": bson.A{
				bson.M{"$or": bson.A{
					bson.M{"price": bson.M{"$lt": 10.0}},
					bson.M{"price": bson.M{"$type": bson.A{"null", "bool"}}},
					bson.M{"price": bson.M{"$exists": false}},
				}},
				isDecimal,
			}},
		},
		{
			"or of numbers does not collapse to $in, number",
			predicate.Or{predicate.Eq{Path: price, Value: 9.99}, predicate.Eq{Path: price, Value: 1.5}},
			numfmt.DecimalNumber,
			bson.M{"$or": bson.A{
				bson.M{"$or": bson.A{bson.M{"price": 9.99}, isDecimal}},
				bson.M{"$or": bson.A{bson.M{"price": 1.5}, isDecimal}},
			}},
		},
		{"eq plain text, number", predicate.Eq{Path: price, Value: "apple"}, numfmt.DecimalNumber, bson.M{"price": "apple"}},
		{"eq number, string mode stays plain", predicate.Eq{Path: price, Value: 9.99}, numfmt.DecimalString, bson.M{"price": 9.99}},
		{
			"nested path in the type clause, auto",
			predicate.Eq{Path: []string{"a", "b"}, Value: "9.99"},
			numfmt.DecimalAuto,
			bson.M{"$or": bson.A{bson.M{"a.b": "9.99"}, bson.M{"a.b": bson.M{"$type": "decimal"}}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, toFilter(tt.pred, tt.mode))
		})
	}
}

package mongo

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/predicate"
)

func TestToFilter(t *testing.T) {
	tests := []struct {
		name string
		pred predicate.Node
		want bson.M
	}{
		{"nil matches everything", nil, bson.M{}},
		{"equality", predicate.Eq{Path: []string{"author"}, Value: "x"}, bson.M{"author": "x"}},
		{"nested path is dotted", predicate.Eq{Path: []string{"a", "b"}, Value: 1.0}, bson.M{"a.b": 1.0}},
		{
			"greater than a number: adds higher jq types",
			predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2015.0},
			bson.M{"$or": bson.A{
				bson.M{"year": bson.M{"$gt": 2015.0}},
				bson.M{"year": bson.M{"$type": bson.A{"string", "array", "object"}}},
			}},
		},
		{
			"less than a number: adds lower jq types and missing",
			predicate.Cmp{Path: []string{"year"}, Op: predicate.Lt, Value: 2015.0},
			bson.M{"$or": bson.A{
				bson.M{"year": bson.M{"$lt": 2015.0}},
				bson.M{"year": bson.M{"$type": bson.A{"null", "bool"}}},
				bson.M{"year": bson.M{"$exists": false}},
			}},
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
			bson.M{"$and": bson.A{bson.M{"a": 1.0}, bson.M{"b": "x"}}},
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
			// jq's m is dotall, which is PCRE's s (not PCRE's m); the translation
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
				bson.M{"items": bson.M{"$elemMatch": bson.M{"p": 6.0}}},
				bson.M{"items": bson.M{"$type": "object"}},
			}},
		},
		{
			"or on different fields stays $or",
			predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: 1.0},
				predicate.Eq{Path: []string{"b"}, Value: 2.0},
			},
			bson.M{"$or": bson.A{bson.M{"a": 1.0}, bson.M{"b": 2.0}}},
		},
		{
			"or of two equalities on one field collapses to $in",
			predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: 1.0},
				predicate.Eq{Path: []string{"a"}, Value: 2.0},
			},
			bson.M{"a": bson.M{"$in": bson.A{1.0, 2.0}}},
		},
		{
			"or mixing equality and range stays $or",
			predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: 1.0},
				predicate.Cmp{Path: []string{"a"}, Op: predicate.Gt, Value: 5.0},
			},
			bson.M{"$or": bson.A{
				bson.M{"a": 1.0},
				bson.M{"$or": bson.A{
					bson.M{"a": bson.M{"$gt": 5.0}},
					bson.M{"a": bson.M{"$type": bson.A{"string", "array", "object"}}},
				}},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, toFilter(tt.pred))
		})
	}
}

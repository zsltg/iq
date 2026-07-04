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
			"or",
			predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: 1.0},
				predicate.Eq{Path: []string{"b"}, Value: 2.0},
			},
			bson.M{"$or": bson.A{bson.M{"a": 1.0}, bson.M{"b": 2.0}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, toFilter(tt.pred))
		})
	}
}

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

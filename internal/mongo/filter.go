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
	case predicate.And:
		return bson.M{"$and": toFilters(t)}
	case predicate.Or:
		return bson.M{"$or": toFilters(t)}
	default:
		// An unknown node type would be a programming error in the compiler;
		// fall back to matching everything so the client-side jq still runs.
		return bson.M{}
	}
}

// toFilters translates each child node, preserving order.
func toFilters(nodes []predicate.Node) bson.A {
	out := make(bson.A, len(nodes))
	for i, n := range nodes {
		out[i] = toFilter(n)
	}
	return out
}

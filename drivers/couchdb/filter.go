package couchdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zsltg/iq/internal/predicate"
)

// ScanFiltered streams only the documents a Mango _find pre-selects for pred,
// letting the server narrow the scan. pred is a conservative superset (the caller
// re-runs the full jq per page), so returning extra documents is safe and returning
// too few is not. When pred compiles to nothing that narrows (an operator whose
// Mango semantics could wrongly exclude a jq match, e.g. !=, regex, length), the
// scan falls back to a plain _all_docs walk, which is both correct and cheaper than
// a match-everything _find.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	if s.db == "" {
		return errNoDatabase
	}
	sel, narrowing := toSelector(pred)
	if !narrowing {
		return s.ScanBatches(ctx, fn)
	}
	return s.findPaged(ctx, sel, fn)
}

// findPaged pages a Mango _find over the selector, handing fn each page of
// {_id: document}. It follows the reply bookmark until a short page ends the walk,
// so a streaming caller holds only one page in memory. Design documents are skipped.
func (s *Store) findPaged(ctx context.Context, selector map[string]any, fn func(batch map[string]any) error) error {
	db := s.client.DB(s.db)
	bookmark := ""
	for {
		query := map[string]any{"selector": selector, "limit": s.pageSize}
		if bookmark != "" {
			query["bookmark"] = bookmark
		}
		rows := db.Find(ctx, query)

		page := make(map[string]any, s.pageSize)
		n := 0
		for rows.Next() {
			n++
			var raw json.RawMessage
			if err := rows.ScanDoc(&raw); err != nil {
				_ = rows.Close()
				return fmt.Errorf("couchdb scan document: %w", err)
			}
			doc, err := decodeDoc(raw, s.decimal)
			if err != nil {
				_ = rows.Close()
				return err
			}
			// A _find row does not expose ID() the way _all_docs does; the document
			// carries its own _id.
			id, _ := doc["_id"].(string)
			if strings.HasPrefix(id, designPrefix) {
				continue
			}
			page[id] = doc
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("couchdb find: %w", err)
		}
		md, mdErr := rows.Metadata()
		_ = rows.Close()

		if len(page) > 0 {
			if err := fn(page); err != nil {
				return err
			}
		}
		// A page shorter than the limit is the last one. Otherwise follow the
		// bookmark the server returned to fetch the next page.
		if n < s.pageSize {
			return nil
		}
		if mdErr != nil || md.Bookmark == "" {
			return nil
		}
		bookmark = md.Bookmark
	}
}

// mangoTypes lists the Mango $type names in jq's total order, so index i is the
// type jq ranks at position i (null lowest, object highest). CouchDB's collation
// order matches jq's, so this reproduces jq's cross-type comparisons.
var mangoTypes = []string{"null", "boolean", "number", "string", "array", "object"}

// mangoOp maps a range operator to its Mango query operator.
var mangoOp = map[predicate.Op]string{
	predicate.Gt: "$gt",
	predicate.Ge: "$gte",
	predicate.Lt: "$lt",
	predicate.Le: "$lte",
}

// toSelector translates a neutral predicate into a Mango selector. The second
// return is whether the selector actually narrows the scan: false means the node
// (or an unsafe part of it) could not be pushed without risking wrongly excluding a
// jq match, so the caller must fall back to a full scan rather than trust the
// selector. Only pure-superset, over-inclusion-safe nodes are pushed; the exact
// negations (!=, NoneMatch) and the polymorphic length/regex/any tests, whose Mango
// semantics could exclude a document jq would keep, deliberately do not narrow.
func toSelector(n predicate.Node) (map[string]any, bool) {
	switch t := n.(type) {
	case predicate.Eq:
		return eqSelector(field(t.Path), t.Value), true
	case predicate.Cmp:
		return cmpSelector(t), true
	case predicate.Exists:
		return map[string]any{field(t.Path): map[string]any{"$exists": true}}, true
	case predicate.NotExists:
		// Exact: a missing field is exactly what jq's `has | not` tests, and Mango's
		// $exists:false matches precisely the absent field.
		return map[string]any{field(t.Path): map[string]any{"$exists": false}}, true
	case predicate.And:
		return andSelector(t)
	case predicate.Or:
		return orSelector(t)
	default:
		// Ne, NoneMatch, Size, Regex, ElemMatch, and any unknown node: do not narrow.
		return nil, false
	}
}

// eqSelector builds a field-equality selector. A nil value must also match an
// absent field, because jq reads a missing field as null and `== null` is true for
// it, whereas Mango's $eq:null matches only an explicit null.
func eqSelector(path string, value any) map[string]any {
	if value == nil {
		return map[string]any{"$or": []any{
			map[string]any{path: nil},
			map[string]any{path: map[string]any{"$exists": false}},
		}}
	}
	return map[string]any{path: value}
}

// cmpSelector translates a range comparison into a conservative superset that
// reproduces jq's cross-type ordering. jq compares across types (null < boolean <
// number < string < array < object), so `field > value` also matches every value
// whose type ranks strictly above the literal's, and `field < value` also matches
// every type strictly below plus a missing field (jq reads a missing field as null,
// the lowest value). The same-type bound uses the native operator; the cross-type
// parts are added as explicit $type clauses. Extra matches are harmless because the
// caller re-runs the full jq; missing one would be a bug, which is why the type
// clauses are exhaustive.
func cmpSelector(c predicate.Cmp) map[string]any {
	path := field(c.Path)
	clauses := []any{map[string]any{path: map[string]any{mangoOp[c.Op]: c.Value}}}

	// The literal is always a number (rank 2) or string (rank 3), so the type lists
	// below are always non-empty.
	rank := valueRank(c.Value)
	if c.Op == predicate.Gt || c.Op == predicate.Ge {
		for _, ty := range mangoTypes[rank+1:] {
			clauses = append(clauses, map[string]any{path: map[string]any{"$type": ty}})
		}
	} else {
		for _, ty := range mangoTypes[:rank] {
			clauses = append(clauses, map[string]any{path: map[string]any{"$type": ty}})
		}
		// A missing field is null in jq, so it is below any number or string.
		clauses = append(clauses, map[string]any{path: map[string]any{"$exists": false}})
	}
	return map[string]any{"$or": clauses}
}

// andSelector conjoins the children that narrow, dropping any that do not (an AND
// with a match-everything term is just the rest). It narrows if at least one child
// does; with none it reduces to nothing pushable.
func andSelector(and predicate.And) (map[string]any, bool) {
	var parts []any
	for _, child := range and {
		if sel, ok := toSelector(child); ok {
			parts = append(parts, sel)
		}
	}
	switch len(parts) {
	case 0:
		return nil, false
	case 1:
		return parts[0].(map[string]any), true
	default:
		return map[string]any{"$and": parts}, true
	}
}

// orSelector disjoins the children. An OR narrows only if *every* branch does:
// a branch that cannot be pushed matches everything, which would make the whole OR
// match everything, so a single unpushable branch drops the OR to a full scan.
func orSelector(or predicate.Or) (map[string]any, bool) {
	parts := make([]any, 0, len(or))
	for _, child := range or {
		sel, ok := toSelector(child)
		if !ok {
			return nil, false
		}
		parts = append(parts, sel)
	}
	if len(parts) == 0 {
		return nil, false
	}
	return map[string]any{"$or": parts}, true
}

// valueRank returns the jq type rank of a range literal: 2 for a number, 3 for a
// string (the only two kinds the compiler emits).
func valueRank(v any) int {
	if _, ok := v.(string); ok {
		return 3
	}
	return 2
}

// field renders a predicate path as a Mango dotted field reference.
func field(path []string) string {
	return strings.Join(path, ".")
}

// compile-time assertion that Store satisfies the optional filtered-scan capability.
var _ interface {
	ScanFiltered(context.Context, predicate.Node, func(map[string]any) error) error
} = (*Store)(nil)

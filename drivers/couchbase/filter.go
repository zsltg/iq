package couchbase

import (
	"context"
	"fmt"
	"strings"

	"github.com/zsltg/iq/internal/predicate"
)

// ScanFiltered streams only the documents a SQL++ WHERE pre-selects for pred, letting
// the query service narrow the scan. pred is a conservative superset (the caller
// re-runs the full jq per page), so returning extra documents is safe and returning
// too few is not. When pred compiles to nothing that narrows — an operator whose SQL++
// semantics could wrongly exclude a jq match (!=, regex, length, element match) — the
// scan falls back to the plain keyset walk, which is correct and no more expensive.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	if s.collection == nil {
		return errNoBucket
	}
	where, params, narrowing := toWhere(pred)
	if !narrowing {
		return s.ScanBatches(ctx, fn)
	}
	return s.scan(ctx, where, params, fn)
}

// rangeOp maps a range operator to its SQL++ comparison symbol.
var rangeOp = map[predicate.Op]string{
	predicate.Gt: ">",
	predicate.Ge: ">=",
	predicate.Lt: "<",
	predicate.Le: "<=",
}

// typeAbove and typeBelow list the SQL++ type-check predicates for the jq types that
// rank strictly above / below each range-literal type. jq compares across types
// (null < boolean < number < string < array < object, a missing field folded to null),
// so `field > <number>` must also keep every string/array/object and `field < <number>`
// every boolean/null/missing. SQL++ comparison operators are type-restricted (a
// cross-type compare yields NULL and is filtered out), so those types are re-included
// with explicit ISTYPE clauses — a superset the client re-run trims.
var (
	typeAboveNumber = []string{"ISSTRING(%s)", "ISARRAY(%s)", "ISOBJECT(%s)"}
	typeAboveString = []string{"ISARRAY(%s)", "ISOBJECT(%s)"}
	typeBelowNumber = []string{"ISBOOLEAN(%s)", "%s IS NULL", "%s IS MISSING"}
	typeBelowString = []string{"ISNUMBER(%s)", "ISBOOLEAN(%s)", "%s IS NULL", "%s IS MISSING"}
)

// toWhere translates a neutral predicate into a SQL++ WHERE fragment and its named
// parameters. The final bool is whether the fragment actually narrows the scan: false
// means the node (or an unsafe part of it) could not be pushed without risking wrongly
// excluding a jq match, so the caller must fall back to a full scan. Only pure-superset
// nodes are pushed: equality, ranges, and existence. Every value rides as a named
// parameter ($p0, $p1, …), never concatenated into the statement.
func toWhere(n predicate.Node) (string, map[string]any, bool) {
	b := &whereBuilder{params: map[string]any{}}
	clause, ok := b.node(n)
	if !ok {
		return "", nil, false
	}
	return clause, b.params, true
}

// whereBuilder accumulates the named parameters a WHERE fragment binds, allocating a
// fresh $pN name per value so no two values collide and none clashes with the scan's
// reserved $after/$page.
type whereBuilder struct {
	params map[string]any
	n      int
}

// param binds a value to a fresh named parameter and returns its $pN reference.
func (b *whereBuilder) param(v any) string {
	name := fmt.Sprintf("p%d", b.n)
	b.n++
	b.params[name] = v
	return "$" + name
}

// node translates one predicate node, returning the SQL++ fragment and whether it
// narrows. It mirrors the couchdb toSelector switch, pushing only over-inclusion-safe
// nodes and declining the rest (which drop the whole scan to a full walk).
func (b *whereBuilder) node(n predicate.Node) (string, bool) {
	switch t := n.(type) {
	case predicate.Eq:
		return b.eq(t)
	case predicate.Cmp:
		return b.cmp(t)
	case predicate.Exists:
		if ref, ok := fieldRef(t.Path); ok {
			return ref + " IS NOT MISSING", true
		}
		return "", false
	case predicate.NotExists:
		if ref, ok := fieldRef(t.Path); ok {
			return ref + " IS MISSING", true
		}
		return "", false
	case predicate.And:
		return b.and(t)
	case predicate.Or:
		return b.or(t)
	default:
		// Ne, Regex, Size, ElemMatch, NoneMatch, and any unknown node: do not narrow.
		return "", false
	}
}

// eq translates an equality. A nil literal must also match an absent field, because jq
// reads a missing field as null and `== null` is true for it, whereas SQL++'s `= NULL`
// is never true.
func (b *whereBuilder) eq(e predicate.Eq) (string, bool) {
	ref, ok := fieldRef(e.Path)
	if !ok {
		return "", false
	}
	if e.Value == nil {
		return fmt.Sprintf("(%s IS NULL OR %s IS MISSING)", ref, ref), true
	}
	return fmt.Sprintf("%s = %s", ref, b.param(e.Value)), true
}

// cmp translates a range comparison into a conservative superset that reproduces jq's
// cross-type ordering: the native operator for the same-type bound, plus explicit
// type-check clauses for every type that ranks above (for >/>=) or below (for </<=) the
// literal. The literal is always a number or a string (the only kinds the compiler
// emits), so the type lists are fixed.
func (b *whereBuilder) cmp(c predicate.Cmp) (string, bool) {
	ref, ok := fieldRef(c.Path)
	if !ok {
		return "", false
	}
	clauses := []string{fmt.Sprintf("%s %s %s", ref, rangeOp[c.Op], b.param(c.Value))}

	_, isString := c.Value.(string)
	var extra []string
	switch c.Op {
	case predicate.Gt, predicate.Ge:
		if isString {
			extra = typeAboveString
		} else {
			extra = typeAboveNumber
		}
	default:
		if isString {
			extra = typeBelowString
		} else {
			extra = typeBelowNumber
		}
	}
	for _, tmpl := range extra {
		clauses = append(clauses, fmt.Sprintf(tmpl, ref))
	}
	return "(" + strings.Join(clauses, " OR ") + ")", true
}

// and conjoins the children that narrow, dropping any that do not (an AND with a
// match-everything term is just the rest). It narrows if at least one child does.
func (b *whereBuilder) and(and predicate.And) (string, bool) {
	var parts []string
	for _, child := range and {
		if clause, ok := b.node(child); ok {
			parts = append(parts, clause)
		}
	}
	switch len(parts) {
	case 0:
		return "", false
	case 1:
		return parts[0], true
	default:
		return "(" + strings.Join(parts, " AND ") + ")", true
	}
}

// or disjoins the children. An OR narrows only if every branch does: a branch that
// cannot be pushed matches everything, which would make the whole OR match everything,
// so one unpushable branch drops the OR to a full scan. An all-equality OR over one
// field collapses to a single IN clause.
func (b *whereBuilder) or(or predicate.Or) (string, bool) {
	if len(or) == 0 {
		return "", false
	}
	if clause, ok := b.orIn(or); ok {
		return clause, true
	}
	parts := make([]string, 0, len(or))
	for _, child := range or {
		clause, ok := b.node(child)
		if !ok {
			return "", false
		}
		parts = append(parts, clause)
	}
	return "(" + strings.Join(parts, " OR ") + ")", true
}

// orIn collapses an OR whose every branch is a non-nil equality on the same field into
// a single `field IN $p` clause, the natural SQL++ form. It declines (false) any OR
// that is not exactly that shape, leaving the general disjunction to handle it.
func (b *whereBuilder) orIn(or predicate.Or) (string, bool) {
	var ref string
	values := make([]any, 0, len(or))
	for i, child := range or {
		eq, isEq := child.(predicate.Eq)
		if !isEq || eq.Value == nil {
			return "", false
		}
		r, ok := fieldRef(eq.Path)
		if !ok {
			return "", false
		}
		if i == 0 {
			ref = r
		} else if r != ref {
			return "", false
		}
		values = append(values, eq.Value)
	}
	return fmt.Sprintf("%s IN %s", ref, b.param(values)), true
}

// fieldRef renders a predicate path as a backtick-quoted SQL++ field reference under
// the scan alias t (`t`.`a`.`b`). pushdown.safeField already rejects a path component
// with a dot or a $ prefix; this additionally refuses a backtick so a component can
// never break out of its quotes, declining (false) rather than emitting an unsafe ref.
func fieldRef(path []string) (string, bool) {
	parts := []string{"`t`"}
	for _, p := range path {
		if strings.Contains(p, "`") {
			return "", false
		}
		parts = append(parts, "`"+p+"`")
	}
	return strings.Join(parts, "."), true
}

// compile-time assertion that Store satisfies the optional filtered-scan capability.
var _ interface {
	ScanFiltered(context.Context, predicate.Node, func(map[string]any) error) error
} = (*Store)(nil)

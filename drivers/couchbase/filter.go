package couchbase

import (
	"context"
	"fmt"
	"strings"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/pushdown"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/rawpred"
)

// ScanFiltered lets the query service narrow the scan with a SQL++ WHERE for
// pred. See query.FilteredScanner.
//
// If pred has no safe WHERE, the scan walks the whole collection with an empty
// WHERE. This happens for an operator whose SQL++ semantics can wrongly exclude
// a jq match (!=, regex, length, element match). A decimal mode that decodes a
// stored number to a string also keeps a leaf out of the WHERE. See
// pushdown.HidesDecodedNumber.
//
// Whatever the WHERE leaves behind, a client-side rawpred prefilter drops rows
// that the full predicate provably rejects, before the UseNumber decode. It does
// not run if the WHERE already captures pred exactly, and the matcher is then
// nil. A document ID is KV metadata and never enters the value, so rawpred reads
// exactly the document.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	if s.collection == nil {
		return errNoBucket
	}
	// An empty where is a full keyset walk, so the fallback needs no branch.
	where, params, _ := toWhere(pred, s.decimal)
	return s.scan(ctx, scanSpec{where: where, params: params, matcher: s.prefilterMatcher(pred)}, fn)
}

// prefilterMatcher builds the rawpred matcher of a scan, with the decimal mode of
// the store. A nil matcher means no prefilter.
func (s *Store) prefilterMatcher(pred predicate.Node) *rawpred.Matcher {
	if prefilterStage(pred, s.decimal).State != query.StageOn {
		return nil
	}
	return rawpred.NewMatcher(pred, s.decimal)
}

// prefilterStage decides the rawpred prefilter from the whole predicate. ScanFiltered
// and ExplainPlan both call it, so the plan cannot drift from the scan. The prefilter
// is off for a nil predicate and for a WHERE that captures the predicate exactly.
func prefilterStage(pred predicate.Node, mode numfmt.DecimalMode) query.StagePlan {
	switch {
	case pred == nil:
		return query.StagePlan{State: query.StageOff, Note: "no compiled predicate"}
	case exactWhere(pred, mode):
		return query.StagePlan{State: query.StageOff, Note: "the WHERE captures the predicate exactly"}
	default:
		return query.StagePlan{State: query.StageOn, Scope: "rawpred on the raw value of each row, before decode"}
	}
}

// exactWhere reports whether toWhere captures n exactly. The WHERE then returns
// precisely the jq matches, so a client-side prefilter only re-confirms them.
// Every node that toWhere pushes is exact: equality (a `null` literal widened to
// null-or-missing, measured 0.0% over-return), the ISTYPE-widened range (measured
// 0.0%), and existence. Exactness is lost when a conjunct is dropped or a node is
// refused. An And is exact only if every conjunct is, and an Or only if every
// branch is. A node that toWhere cannot push (!=, regex, length, element match, any
// unknown node) is never exact. A field path that toWhere refuses (a backtick
// component) is not exact either.
func exactWhere(n predicate.Node, mode numfmt.DecimalMode) bool {
	if pushdown.HidesDecodedNumber(n, mode) {
		return false
	}
	switch t := n.(type) {
	case predicate.Eq:
		_, ok := fieldRef(t.Path)
		return ok
	case predicate.Cmp:
		_, ok := fieldRef(t.Path)
		return ok
	case predicate.Exists:
		_, ok := fieldRef(t.Path)
		return ok
	case predicate.NotExists:
		_, ok := fieldRef(t.Path)
		return ok
	case predicate.And:
		return allExact(t, mode)
	case predicate.Or:
		return allExact(t, mode)
	default:
		// Ne, Regex, Size, ElemMatch, NoneMatch, and any unknown node: not pushed, so
		// never exact. The prefilter must run.
		return false
	}
}

// allExact reports whether every child of a non-empty composite is exact. An empty
// composite is never exact, because toWhere declines it as non-narrowing.
func allExact(children []predicate.Node, mode numfmt.DecimalMode) bool {
	if len(children) == 0 {
		return false
	}
	for _, c := range children {
		if !exactWhere(c, mode) {
			return false
		}
	}
	return true
}

// rangeOp maps a range operator to its SQL++ comparison symbol.
var rangeOp = map[predicate.Op]string{
	predicate.Gt: ">",
	predicate.Ge: ">=",
	predicate.Lt: "<",
	predicate.Le: "<=",
}

// typeAbove and typeBelow list the SQL++ type checks for the jq types that rank
// above and below each range-literal type. jq compares across types. The order is
// null < boolean < number < string < array < object, with a missing field folded
// to null. SQL++ comparisons are type-restricted: a cross-type compare yields NULL.
// So the ISTYPE clauses re-include those types, and the client re-run trims them.
var (
	typeAboveNumber = []string{"ISSTRING(%s)", "ISARRAY(%s)", "ISOBJECT(%s)"}
	typeAboveString = []string{"ISARRAY(%s)", "ISOBJECT(%s)"}
	typeBelowNumber = []string{"ISBOOLEAN(%s)", "%s IS NULL", "%s IS MISSING"}
	typeBelowString = []string{"ISNUMBER(%s)", "ISBOOLEAN(%s)", "%s IS NULL", "%s IS MISSING"}
)

// toWhere translates a neutral predicate into a SQL++ WHERE fragment and its named
// parameters. The final bool is false if the fragment does not narrow the scan.
// That happens when the node, or an unsafe part of it, cannot be pushed without
// risk of wrongly excluding a jq match. Only pure-superset nodes are pushed:
// equality, ranges, and existence. Every value is a named parameter ($p0, $p1,
// ...), never concatenated.
func toWhere(n predicate.Node, mode numfmt.DecimalMode) (string, map[string]any, bool) {
	b := &whereBuilder{params: map[string]any{}, mode: mode}
	clause, ok := b.node(n)
	if !ok {
		return "", nil, false
	}
	return clause, b.params, true
}

// whereBuilder accumulates the named parameters a WHERE fragment binds. It
// allocates a fresh $pN name per value, so no two values collide and none clashes
// with the scan's reserved $after/$page.
type whereBuilder struct {
	params map[string]any
	n      int
	mode   numfmt.DecimalMode
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
	if pushdown.HidesDecodedNumber(n, b.mode) {
		return "", false
	}
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

// eq translates an equality. A nil literal must also match an absent field. This
// is because jq reads a missing field as null, so `== null` is true for it.
// SQL++'s `= NULL` is never true.
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

// cmp translates a range comparison into a conservative superset that reproduces
// jq's cross-type ordering. It uses the native operator for the same-type bound. It
// adds explicit type-check clauses for every type that ranks above (for >/>=) or
// below (for </<=) the literal. The literal is always a number or a string (the
// only kinds the compiler emits), so the type lists are fixed.
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
// match-everything term is only the rest). It narrows if at least one child does.
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

// or disjoins the children. An OR narrows only if every branch does. A branch that
// cannot be pushed matches everything. That makes the whole OR match everything, so
// one unpushable branch drops the OR to a full scan. An all-equality OR over one
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
		if !isEq || eq.Value == nil || pushdown.HidesDecodedNumber(eq, b.mode) {
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

// fieldRef renders a predicate path as a backtick-quoted SQL++ field reference
// under the scan alias t (`t`.`a`.`b`). The function pushdown.safeField already
// rejects a path component with a dot or a $ prefix. This also refuses a backtick
// and a backslash. A backtick ends the quotes, and SQL++ reads a backslash as an
// escape inside them. A component with either character returns false, so the
// predicate is not pushed and jq runs it on the client.
func fieldRef(path []string) (string, bool) {
	parts := []string{"`t`"}
	for _, p := range path {
		if strings.ContainsAny(p, "`\\") {
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

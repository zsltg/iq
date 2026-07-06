package neo4j

import (
	"context"
	"fmt"
	"strings"

	"github.com/zsltg/iq/internal/predicate"
)

// ScanFiltered streams only the nodes a Cypher WHERE clause pre-selects for pred,
// letting the server narrow the scan. pred is a conservative superset (the caller
// re-runs the full jq per page), so returning extra nodes is safe and returning too
// few is not. When pred compiles to nothing that narrows — a nested path Neo4j has
// no property for, or an operator whose Cypher semantics could wrongly exclude a jq
// match — the scan falls back to a plain label walk, which is both correct and
// cheaper than a match-everything filter.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	if s.target.name == "" {
		return errNoLabel
	}
	b := &cypherBuilder{params: map[string]any{}, variable: s.target.variable()}
	where, narrowing := b.translate(pred)
	if !narrowing {
		return s.ScanBatches(ctx, fn)
	}
	return s.pagedScan(ctx, where, b.params, fn)
}

// cypherBuilder accumulates the parameters for a translated predicate, naming them
// f0, f1, … so they never collide with the scan's own after/limit/key parameters.
// variable is the entity the WHERE clause is over (n for a node, r for a
// relationship).
type cypherBuilder struct {
	n        int
	params   map[string]any
	variable string
}

// param binds v to a fresh f-name and returns the $-reference for the statement.
func (b *cypherBuilder) param(v any) string {
	name := fmt.Sprintf("f%d", b.n)
	b.n++
	b.params[name] = v
	return "$" + name
}

// translate renders a neutral predicate as a Cypher boolean expression over the
// bound node n. The second return is whether the expression actually narrows the
// scan: false means the node (or an unsafe part of it) could not be pushed without
// risking wrongly excluding a jq match, so the caller must fall back to a full scan.
// Only pure-superset, over-inclusion-safe nodes are pushed: equality, key presence,
// and their AND/OR combinations. Range comparisons (Cmp) are deliberately not
// pushed — Cypher compares different types as null rather than by jq's cross-type
// ordering, so a range clause could exclude a value jq keeps; the exact negations
// (Ne, NoneMatch) and the polymorphic length/regex/any tests do not narrow either.
func (b *cypherBuilder) translate(n predicate.Node) (string, bool) {
	switch t := n.(type) {
	case predicate.Eq:
		if !pushablePath(t.Path) {
			return "", false
		}
		prop := b.param(t.Path[0])
		if t.Value == nil {
			// jq reads a missing field as null and `== null` is true for it; a Neo4j
			// property is absent rather than explicitly null, so IS NULL matches exactly.
			return fmt.Sprintf("%s[%s] IS NULL", b.variable, prop), true
		}
		return fmt.Sprintf("%s[%s] = %s", b.variable, prop, b.param(t.Value)), true
	case predicate.Exists:
		if !pushablePath(t.Path) {
			return "", false
		}
		return fmt.Sprintf("%s[%s] IS NOT NULL", b.variable, b.param(t.Path[0])), true
	case predicate.NotExists:
		if !pushablePath(t.Path) {
			return "", false
		}
		return fmt.Sprintf("%s[%s] IS NULL", b.variable, b.param(t.Path[0])), true
	case predicate.And:
		return b.andClause(t)
	case predicate.Or:
		return b.orClause(t)
	default:
		// Cmp, Ne, NoneMatch, Size, Regex, ElemMatch, and any unknown node: do not narrow.
		return "", false
	}
}

// andClause conjoins the children that narrow, dropping any that do not (an AND with
// a match-everything term is just the rest). It narrows if at least one child does.
func (b *cypherBuilder) andClause(and predicate.And) (string, bool) {
	var parts []string
	for _, child := range and {
		if expr, ok := b.translate(child); ok {
			parts = append(parts, expr)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return "(" + strings.Join(parts, " AND ") + ")", true
}

// orClause disjoins the children. An OR narrows only if every branch does: a branch
// that cannot be pushed matches everything, which would make the whole OR match
// everything, so a single unpushable branch drops the OR to a full scan.
func (b *cypherBuilder) orClause(or predicate.Or) (string, bool) {
	if len(or) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(or))
	for _, child := range or {
		expr, ok := b.translate(child)
		if !ok {
			return "", false
		}
		parts = append(parts, expr)
	}
	return "(" + strings.Join(parts, " OR ") + ")", true
}

// pushablePath reports whether a predicate path names a single stored property that
// can be pushed. Neo4j properties are flat, so a nested path (.author.name) has no
// property to match; the reserved envelope keys (_id/_labels/_type/_start/_end) are
// computed, not stored, so they are not pushable either.
func pushablePath(path []string) bool {
	if len(path) != 1 {
		return false
	}
	_, isReserved := reserved[path[0]]
	return !isReserved
}

// compile-time assertion that Store satisfies the optional filtered-scan capability.
var _ interface {
	ScanFiltered(context.Context, predicate.Node, func(map[string]any) error) error
} = (*Store)(nil)

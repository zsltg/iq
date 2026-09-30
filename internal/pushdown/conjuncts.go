package pushdown

import (
	"fmt"

	"github.com/itchyny/gojq"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/selector"
)

// Conjunct is one top-level conjunct of a streamable filter's select(...)
// predicate together with the pushdown compiler's decision about it. Expr is the
// conjunct's jq source, for display. Pred is the pushable predicate the compiler
// extracted from it, or nil when the conjunct cannot be pushed; Reason then carries
// a short, backend-neutral explanation of why. A caller offers a non-nil Pred to a
// backend translator, which may still decline it — so Pred means "the compiler
// could push this", not "the backend did".
type Conjunct struct {
	Expr   string
	Pred   predicate.Node
	Reason string
}

// Conjuncts splits the select(...) predicates of a streamable `.[]`-rooted filter
// that test the element itself (see elementSelects) into their top-level conjuncts and reports, per conjunct, whether the compiler could
// turn it into a pushable predicate and, when it could not, why. Multiple selects
// contribute their conjuncts in order; a select whose argument is a top-level `and`
// contributes each side separately, so the breakdown matches what Compile would
// push (the And of the compiled conjuncts). It returns ok=false when the filter is
// not a streamable scan or has no select conjunct to analyse — nothing is ever
// pushed then, so the caller shows no breakdown.
func Conjuncts(q *gojq.Query) ([]Conjunct, bool) {
	if !selector.Keys(q).Streamable {
		return nil, false
	}
	var out []Conjunct
	for _, arg := range elementSelects(pipeStages(q)) {
		for _, c := range topConjuncts(arg) {
			cj := Conjunct{Expr: c.String()}
			if p, ok := extractPred(c); ok {
				cj.Pred = p
			} else {
				cj.Reason = declineReason(c)
			}
			out = append(out, cj)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// maxNestDepth bounds how deep unwrap peels parenthesizing wrappers before it gives
// up. Real filters nest a handful deep, so the cap only ever fires on a pathological
// or synthetic input, where declining the conjunct is safe (the full jq re-runs
// client-side). Bounding the peel loop by construction also means a mutant that
// removes the loop's progress cannot spin forever — it terminates at the cap.
const maxNestDepth = 512

// topConjuncts splits e into its top-level and-conjuncts, unwrapping parentheses so
// `(a and b) and c` yields [a, b, c]. Splitting stops at any operator other than
// `and`: a comparison, a `path | builtin` pipe, or an `or` stays whole, because its
// parts are not independently droppable conjuncts (an or must push every branch or
// none). An expression nested past maxNestDepth contributes no conjunct: it affects
// only the --explain breakdown, never execution (which compiles independently), and
// the client-side jq still runs.
func topConjuncts(e *gojq.Query) []*gojq.Query {
	u, ok := unwrap(e)
	if !ok {
		return nil
	}
	if u.Op == gojq.OpAnd {
		return append(topConjuncts(u.Left), topConjuncts(u.Right)...)
	}
	return []*gojq.Query{u}
}

// unwrap strips parenthesizing query wrappers (`(expr)`), which a piped clause
// carries when combined, so the conjunct is analysed and displayed as its inner
// expression. It mirrors the first unwrap step extractPred performs. The loop is
// bounded by maxNestDepth: nesting deeper than that returns ok=false so the caller
// declines rather than loops forever.
func unwrap(e *gojq.Query) (*gojq.Query, bool) {
	for depth := 0; depth <= maxNestDepth; depth++ {
		if e.Op != 0 || e.Term == nil || e.Term.Type != gojq.TermTypeQuery || len(e.Term.SuffixList) != 0 {
			return e, true
		}
		e = e.Term.Query
	}
	return nil, false
}

// declineReason returns a short, backend-neutral explanation of why the pushdown
// compiler could not turn conjunct e into a pushable predicate. It inspects only
// the conjunct's top-level shape, mapping the compiler's own gate points — an
// inexact negation, a non-portable regex, an unsafe field name — to a human reason,
// and reports anything else as an unpushable construct. It is best-effort display
// text, never a control signal, so an imperfect classification only affects wording.
func declineReason(e *gojq.Query) string {
	// e arrives already unwrapped from topConjuncts, so it is inspected directly.
	// A trailing `| not` the compiler could not extract exactly (a negation cannot
	// widen, so an inexact inner predicate is dropped entirely).
	if stages := pipeStages(e); len(stages) >= 2 && isNot(stages[len(stages)-1]) {
		return "negation is not exactly expressible"
	}
	// A `.path | test(re)` whose pattern or flags are not portable across engines.
	if isTestPipe(e) {
		return "regex is not portable across backends"
	}
	// A comparison or membership on a field name a backend cannot safely push.
	if name, ok := unsafeFieldName(e); ok {
		return fmt.Sprintf("field %q is dotted or $-prefixed, unsafe to push", name)
	}
	return "not a pushable comparison"
}

// isTestPipe reports whether e is a `path | test(...)` pipe. Reached only after the
// compiler declined e, so a match means the regex gate refused the pattern or flags.
func isTestPipe(e *gojq.Query) bool {
	if e.Op != gojq.OpPipe {
		return false
	}
	rhs := e.Right
	if rhs.Op != 0 || rhs.Term == nil || rhs.Term.Func == nil || len(rhs.Term.SuffixList) != 0 {
		return false
	}
	return rhs.Term.Func.Name == "test"
}

// unsafeFieldName returns the first field name in a comparison or membership
// conjunct that the compiler refused because it is unsafe to push: a name with a
// dot (a backend joining the path with dots would read it as a nested path) or one
// beginning with '$' (it could land in operator position in a document-query
// backend). A conjunct with no resolvable field path returns ok=false and falls
// through to the generic reason.
func unsafeFieldName(e *gojq.Query) (string, bool) {
	switch e.Op {
	case gojq.OpEq, gojq.OpNe, gojq.OpGt, gojq.OpGe, gojq.OpLt, gojq.OpLe:
		if name, ok := firstUnsafeComponent(e.Left); ok {
			return name, true
		}
		return firstUnsafeComponent(e.Right)
	case gojq.OpPipe:
		return firstUnsafeComponent(e.Left)
	default:
		return "", false
	}
}

// firstUnsafeComponent returns the first component of q's leading field path that
// safeField rejects, or ok=false when q is not a plain field path or every
// component is safe.
func firstUnsafeComponent(q *gojq.Query) (string, bool) {
	for _, c := range rawPath(q) {
		if !safeField(c) {
			return c, true
		}
	}
	return "", false
}

// rawPath returns the field path of a plain relative index expression, like pathOf
// but without the safeField gate, so declineReason can name the exact component
// that made the path unsafe. It returns nil for anything with iteration, a computed
// index, or extra operators.
func rawPath(q *gojq.Query) []string {
	if q.Op != 0 || q.Term == nil || len(q.FuncDefs) != 0 {
		return nil
	}
	t := q.Term
	if t.Type != gojq.TermTypeIndex || t.Index == nil {
		return nil
	}
	name, ok := indexName(t.Index)
	if !ok {
		return nil
	}
	path := []string{name}
	for _, s := range t.SuffixList {
		if s.Iter || s.Index == nil {
			return nil
		}
		name, ok := indexName(s.Index)
		if !ok {
			return nil
		}
		path = append(path, name)
	}
	return path
}

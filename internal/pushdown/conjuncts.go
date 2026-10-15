package pushdown

import (
	"fmt"

	"github.com/itchyny/gojq"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/selector"
)

// Conjunct is one top-level conjunct of a streamable filter's select(...) predicate,
// with the pushdown compiler's decision about it. Expr is the conjunct's jq source, for
// display. Pred is the pushable predicate that the compiler extracted, or nil when the
// conjunct cannot be pushed. Reason then gives a short, backend-neutral explanation. A
// backend translator can still decline a non-nil Pred. So a non-nil Pred means that the
// compiler can push the conjunct, not that the backend did.
type Conjunct struct {
	Expr   string
	Pred   predicate.Node
	Reason string
}

// Conjuncts splits the select(...) predicates of a streamable `.[]`-rooted filter that
// test the element itself (see elementSelects) into their top-level conjuncts. For each
// conjunct it reports whether the compiler can turn it into a pushable predicate and,
// if not, why. Multiple selects contribute their conjuncts in order. A select whose
// argument is a top-level `and` contributes each side separately, so the breakdown
// matches what Compile pushes (the And of the compiled conjuncts). Conjuncts returns
// ok=false if the filter is not a streamable scan or has no select conjunct to analyse.
// Nothing is pushed then, so the caller shows no breakdown.
func Conjuncts(q *gojq.Query) ([]Conjunct, bool) {
	if !selector.Keys(q).Streamable || shadowsAny(q) {
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

// maxNestDepth bounds how deep unwrap peels parenthesizing wrappers before it gives up.
// Real filters nest a handful deep. So the cap fires only on a pathological or
// synthetic input, where declining the conjunct is safe (the full jq re-runs
// client-side). Bounding the peel loop by construction also means that a mutant that
// removes the progress of the loop cannot spin forever. It terminates at the cap.
const maxNestDepth = 512

// topConjuncts splits e into its top-level and-conjuncts and unwraps parentheses, so
// `(a and b) and c` yields [a, b, c]. Splitting stops at any other operator: a
// comparison, a `path | builtin` pipe, or an `or` stays whole, because its parts are
// not independently droppable. An or must push every branch or none. An expression
// nested past maxNestDepth contributes no conjunct. This affects only the --explain
// breakdown, never execution, which compiles independently. The client-side jq still
// runs.
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
		inner, ok := parenQuery(e)
		if !ok {
			return e, true
		}
		e = inner
	}
	return nil, false
}

// declineReason returns a short, backend-neutral explanation of why the pushdown
// compiler cannot turn conjunct e into a pushable predicate. It inspects only the
// top-level shape of the conjunct. It maps the compiler's own gate points to a human
// reason: an inexact negation, a non-portable regex, an unsafe field name. It reports
// anything else as an unpushable construct. It is best-effort display text, never a
// control signal, so an imperfect classification only affects wording.
func declineReason(e *gojq.Query) string {
	// e arrives already unwrapped from topConjuncts, so it is inspected directly. A
	// trailing `| not` that the compiler cannot extract exactly (a negation cannot
	// widen, so an inexact inner predicate is dropped entirely).
	if _, ok := negatedPipe(e); ok {
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
	f, ok := bareFunc(e.Right)
	if !ok {
		return false
	}
	return f.Name == "test"
}

// unsafeFieldName returns the first field name in a comparison or membership conjunct
// that safeField refuses to push. A conjunct with no resolvable field path returns
// ok=false and falls through to the generic reason.
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
// safeField rejects. It returns ok=false when q is not a plain field path or every
// component is safe.
func firstUnsafeComponent(q *gojq.Query) (string, bool) {
	for _, c := range rawPath(q) {
		if !safeField(c) {
			return c, true
		}
	}
	return "", false
}

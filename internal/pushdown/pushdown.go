// Package pushdown compiles the equality predicate of a jq select(...) filter
// into a backend-neutral predicate, so a backend can pre-filter server-side
// instead of streaming its whole collection. It only extracts what is provably a
// conservative *superset* of the jq semantics — positive equality combined with
// and/or — because the caller always re-runs the full jq client-side, which can
// strip extra results but cannot recover missing ones. Ranges, negation, regex,
// and everything else are left to that client-side pass.
package pushdown

import (
	"strconv"
	"strings"

	"github.com/itchyny/gojq"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/selector"
)

// Compile returns a superset equality predicate for a streamable `.[]`-rooted
// filter's select(...) stages, or ok=false when nothing can be pushed. Multiple
// selects are ANDed; a select whose predicate cannot be compiled is dropped
// (widening the result, still a superset). Only the selects that test the element
// itself count (see elementSelects).
func Compile(q *gojq.Query) (predicate.Node, bool) {
	if !selector.Keys(q).Streamable {
		return nil, false
	}
	var preds []predicate.Node
	for _, e := range elementSelects(pipeStages(q)) {
		if p, ok := extractPred(e); ok {
			preds = append(preds, p)
		}
	}
	switch len(preds) {
	case 0:
		return nil, false
	case 1:
		return preds[0], true
	default:
		return flattenAnd(preds...), true
	}
}

// elementSelects returns the arguments of the select(...) stages that test the
// element itself. stages[0] must be a bare `.[]` (or `.[]?`): a head such as
// `.[].b` already hands a derived value to the next stage. The element reaches a
// stage unchanged only through select(...) and identity (`.`) stages, so the walk
// stops at the first other stage: a select after `.b`, `map(...)` or `{b: .a}`
// tests a derived value, not the element, and a predicate on the element would
// drop an element that the filter keeps. The selects before that stage still hold
// for every element that reaches the output, so their predicates stay a superset.
func elementSelects(stages []*gojq.Query) []*gojq.Query {
	if head := stages[0].String(); head != ".[]" && head != ".[]?" {
		return nil
	}
	var args []*gojq.Query
	for _, s := range stages[1:] {
		if isIdentity(s) {
			continue
		}
		e, ok := selectArg(s)
		if !ok {
			break
		}
		args = append(args, e)
	}
	return args
}

// isIdentity reports whether a stage is exactly `.`, which passes the element on
// unchanged. An operator stage, such as `.a, .b`, has no Term.
func isIdentity(s *gojq.Query) bool {
	return s.Term != nil && s.Term.Type == gojq.TermTypeIdentity && len(s.Term.SuffixList) == 0
}

// pipeStages flattens a pipe chain into its stages in order, so
// `.[] | select(a) | .b` becomes [`.[]`, `select(a)`, `.b`]. gojq builds pipes
// right-associatively (`a | b | c` == `a | (b | c)`), so the chain descends the
// right operand.
func pipeStages(q *gojq.Query) []*gojq.Query {
	if q.Op == gojq.OpPipe {
		return append([]*gojq.Query{q.Left}, pipeStages(q.Right)...)
	}
	return []*gojq.Query{q}
}

// joinPipe rebuilds a pipe chain from stages, the inverse of pipeStages, so a
// prefix of a flattened chain can be re-analysed on its own.
func joinPipe(stages []*gojq.Query) *gojq.Query {
	if len(stages) == 1 {
		return stages[0]
	}
	return &gojq.Query{Op: gojq.OpPipe, Left: stages[0], Right: joinPipe(stages[1:])}
}

// selectArg returns the argument of a stage that is exactly a select(E) call.
func selectArg(s *gojq.Query) (*gojq.Query, bool) {
	if s.Op != 0 || s.Term == nil || len(s.Term.SuffixList) != 0 {
		return nil, false
	}
	f := s.Term.Func
	if f == nil || f.Name != "select" || len(f.Args) != 1 {
		return nil, false
	}
	return f.Args[0], true
}

// extractPred builds a superset predicate from a select's boolean expression, or
// ok=false when it is not one we push. An and may drop an uncompilable conjunct
// (widening); an or must compile every branch (dropping one would lose matches).
func extractPred(e *gojq.Query) (predicate.Node, bool) {
	// Unwrap a parenthesized sub-expression, which a piped clause needs when
	// combined (`.a == 1 and (.name | test("x"))`).
	if inner, ok := parenQuery(e); ok {
		return extractPred(inner)
	}
	// A bare builtin fed the document itself, i.e. has("field").
	if f, ok := bareFunc(e); ok {
		return existsFrom(nil, f)
	}
	// A trailing `| not` negates everything before it (pipes flatten to stages,
	// so `.a | any(x) | not` is `not` applied to `.a | any(x)`).
	if stages := pipeStages(e); len(stages) >= 2 && isNot(stages[len(stages)-1]) {
		return negate(joinPipe(stages[:len(stages)-1]))
	}
	switch e.Op {
	case gojq.OpPipe:
		return pipeAtom(e.Left, e.Right)
	case gojq.OpAnd:
		l, lok := extractPred(e.Left)
		r, rok := extractPred(e.Right)
		switch {
		case lok && rok:
			return flattenAnd(l, r), true
		case lok:
			return l, true
		case rok:
			return r, true
		default:
			return nil, false
		}
	case gojq.OpOr:
		l, lok := extractPred(e.Left)
		r, rok := extractPred(e.Right)
		if lok && rok {
			return flattenOr(l, r), true
		}
		return nil, false
	case gojq.OpEq:
		return eqAtom(e.Left, e.Right)
	case gojq.OpNe:
		return neAtom(e.Left, e.Right)
	case gojq.OpGt:
		return cmpAtom(predicate.Gt, e.Left, e.Right)
	case gojq.OpGe:
		return cmpAtom(predicate.Ge, e.Left, e.Right)
	case gojq.OpLt:
		return cmpAtom(predicate.Lt, e.Left, e.Right)
	case gojq.OpLe:
		return cmpAtom(predicate.Le, e.Left, e.Right)
	default:
		return nil, false
	}
}

// cmpAtom builds a Cmp from a `path OP literal` comparison. When the path is on
// the right (`literal OP path`), the operator is flipped so Value stays on the
// right. Only number and string literals are pushed — a range against a boolean
// or null has no useful type-order superset.
func cmpAtom(op predicate.Op, a, b *gojq.Query) (predicate.Node, bool) {
	path, v, flipped, ok := pathAndLiteral(a, b, rangeLiteral)
	if !ok {
		return nil, false
	}
	if flipped {
		op = flip(op)
	}
	return predicate.Cmp{Path: path, Op: op, Value: v}, true
}

// pathAndLiteral matches a `path OP literal` operand pair in either order. It
// tries `path OP literal` first, then `literal OP path`. lit reads the literal
// operand. flipped is true when the path is on the right.
func pathAndLiteral(a, b *gojq.Query, lit func(*gojq.Query) (any, bool)) (path []string, v any, flipped, ok bool) {
	if path, ok := pathOf(a); ok {
		if v, ok := lit(b); ok {
			return path, v, false, true
		}
	}
	if path, ok := pathOf(b); ok {
		if v, ok := lit(a); ok {
			return path, v, true, true
		}
	}
	return nil, nil, false, false
}

// flip reverses a comparison operator so a `literal OP path` reads as `path OP' literal`.
func flip(op predicate.Op) predicate.Op {
	switch op {
	case predicate.Gt:
		return predicate.Lt
	case predicate.Ge:
		return predicate.Le
	case predicate.Lt:
		return predicate.Gt
	default: // Le
		return predicate.Ge
	}
}

// rangeLiteral returns a comparable literal (number or string) or ok=false. A
// boolean or null literal is not returned, because a range across those has no
// clean type-order superset worth pushing.
func rangeLiteral(q *gojq.Query) (any, bool) {
	v, ok := literalOf(q)
	if !ok {
		return nil, false
	}
	switch v.(type) {
	case float64, string:
		return v, true
	default:
		return nil, false
	}
}

// pipeAtom builds a predicate from a `path | builtin` expression: the value at
// path piped into test(re) (regex), has(key) ($exists), or a length == n
// comparison ($size). Anything else is not pushed.
func pipeAtom(pathQ, rhs *gojq.Query) (predicate.Node, bool) {
	path, ok := pathOf(pathQ)
	if !ok {
		return nil, false
	}
	// .path | length == n
	if rhs.Op == gojq.OpEq {
		if n, ok := lengthEq(rhs.Left, rhs.Right); ok {
			return predicate.Size{Path: path, N: n}, true
		}
		return nil, false
	}
	// .path | test(re) / has(key)
	f, ok := bareFunc(rhs)
	if !ok {
		return nil, false
	}
	switch f.Name {
	case "test":
		return regexFrom(path, f)
	case "has":
		return existsFrom(path, f)
	case "any":
		return elemMatchFrom(path, f)
	default:
		return nil, false
	}
}

// elemMatchFrom builds an ElemMatch for `path | any(condition)`: the array at
// path has an element satisfying condition. Only the one-argument any(condition)
// is handled, and only when the element condition itself compiles.
func elemMatchFrom(path []string, f *gojq.Func) (predicate.Node, bool) {
	if len(f.Args) != 1 {
		return nil, false
	}
	cond, ok := extractPred(f.Args[0])
	if !ok {
		return nil, false
	}
	return predicate.ElemMatch{Path: path, Cond: cond}, true
}

// regexFrom builds a Regex for `path | test(pattern[; flags])`. It is only pushed
// when the pattern uses a subset of regex syntax every engine interprets
// identically (see portableRegex), so the push stays exactly equivalent to jq's
// own Oniguruma engine.
func regexFrom(path []string, f *gojq.Func) (predicate.Node, bool) {
	if len(f.Args) < 1 || len(f.Args) > 2 {
		return nil, false
	}
	pattern, ok := stringLit(f.Args[0])
	if !ok || !portableRegex(pattern) {
		return nil, false
	}
	flags := ""
	if len(f.Args) == 2 {
		flags, ok = stringLit(f.Args[1])
		if !ok || !portableFlags(flags) {
			return nil, false
		}
	}
	return predicate.Regex{Path: path, Pattern: pattern, Flags: flags}, true
}

// existsFrom builds an Exists for has("key"): the field key exists under base
// (empty base means the document root). A key containing a dot is not pushed, as
// a backend joining the path with dots would read it as a nested path.
func existsFrom(base []string, f *gojq.Func) (predicate.Node, bool) {
	if f.Name != "has" || len(f.Args) != 1 {
		return nil, false
	}
	key, ok := stringLit(f.Args[0])
	if !ok || !safeField(key) {
		return nil, false
	}
	path := make([]string, 0, len(base)+1)
	path = append(path, base...)
	path = append(path, key)
	return predicate.Exists{Path: path}, true
}

// neAtom builds a Ne from a `path != literal` comparison in either order.
func neAtom(a, b *gojq.Query) (predicate.Node, bool) {
	path, v, _, ok := pathAndLiteral(a, b, literalOf)
	if !ok {
		return nil, false
	}
	return predicate.Ne{Path: path, Value: v}, true
}

// isNot reports whether q is the bare not builtin.
func isNot(q *gojq.Query) bool {
	f, ok := bareFunc(q)
	return ok && f.Name == "not" && len(f.Args) == 0
}

// bareFunc returns the function call of a query that is exactly one term with
// no operator and no suffix, or ok=false otherwise.
func bareFunc(q *gojq.Query) (*gojq.Func, bool) {
	if q.Op != 0 || q.Term == nil || q.Term.Func == nil || len(q.Term.SuffixList) != 0 {
		return nil, false
	}
	return q.Term.Func, true
}

// parenQuery returns the inner query of a parenthesized term with no operator
// and no suffix, or ok=false otherwise.
func parenQuery(q *gojq.Query) (*gojq.Query, bool) {
	if q.Op != 0 || q.Term == nil || q.Term.Type != gojq.TermTypeQuery || len(q.Term.SuffixList) != 0 {
		return nil, false
	}
	return q.Term.Query, true
}

// negate pushes `inner | not`. A negation cannot use the superset-and-re-run
// safety net (negating a superset yields a subset that silently drops matching
// documents), so it extracts inner through extractExact — which never widens —
// and pushes only equality, existence, or an any() over an exact condition.
func negate(inner *gojq.Query) (predicate.Node, bool) {
	p, ok := extractExact(inner)
	if !ok {
		return nil, false
	}
	switch t := p.(type) {
	case predicate.Eq:
		return predicate.Ne(t), true
	case predicate.Exists:
		return predicate.NotExists(t), true
	case predicate.ElemMatch:
		return predicate.NoneMatch(t), true
	default:
		return nil, false
	}
}

// extractExact builds a predicate that represents e *exactly*, never a widened
// superset, or ok=false when no such predicate exists. It is the negation-safe
// counterpart to extractPred: extractPred may drop an uncompilable conjunct of an
// `and` (safe positively, because the client re-runs jq), but negating that
// widened result yields a subset that drops matching documents. Only positive
// equality, existence, an any() over an exact condition, and an and/or of those
// are exact; a range, regex, !=, size, or a widened `and` is not.
func extractExact(e *gojq.Query) (predicate.Node, bool) {
	// Unwrap a parenthesized sub-expression, matching extractPred.
	if inner, ok := parenQuery(e); ok {
		return extractExact(inner)
	}
	// A bare has("field") fed the document itself.
	if f, ok := bareFunc(e); ok {
		return existsFrom(nil, f)
	}
	switch e.Op {
	case gojq.OpPipe:
		return exactPipe(e.Left, e.Right)
	case gojq.OpAnd:
		l, lok := extractExact(e.Left)
		r, rok := extractExact(e.Right)
		if lok && rok {
			return flattenAnd(l, r), true
		}
		return nil, false
	case gojq.OpOr:
		l, lok := extractExact(e.Left)
		r, rok := extractExact(e.Right)
		if lok && rok {
			return flattenOr(l, r), true
		}
		return nil, false
	case gojq.OpEq:
		return eqAtom(e.Left, e.Right)
	default:
		return nil, false
	}
}

// exactPipe extracts the exact predicate of a `path | builtin` expression, the
// only pipe forms that stay exact under negation: has(key) existence, or an
// any(cond) whose element condition is itself exact.
func exactPipe(pathQ, rhs *gojq.Query) (predicate.Node, bool) {
	path, ok := pathOf(pathQ)
	if !ok {
		return nil, false
	}
	f, ok := bareFunc(rhs)
	if !ok {
		return nil, false
	}
	switch f.Name {
	case "has":
		return existsFrom(path, f)
	case "any":
		if len(f.Args) != 1 {
			return nil, false
		}
		cond, ok := extractExact(f.Args[0])
		if !ok {
			return nil, false
		}
		return predicate.ElemMatch{Path: path, Cond: cond}, true
	default:
		return nil, false
	}
}

// lengthEq recognizes `length == n` (in either order) and returns n when it is a
// non-negative integer.
func lengthEq(a, b *gojq.Query) (int, bool) {
	if isLength(a) {
		return intLiteral(b)
	}
	if isLength(b) {
		return intLiteral(a)
	}
	return 0, false
}

// isLength reports whether q is the bare length builtin.
func isLength(q *gojq.Query) bool {
	f, ok := bareFunc(q)
	return ok && f.Name == "length" && len(f.Args) == 0
}

// intLiteral returns a non-negative integer literal.
func intLiteral(q *gojq.Query) (int, bool) {
	v, ok := literalOf(q)
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	n := int(f)
	if float64(n) != f || n < 0 {
		return 0, false
	}
	return n, true
}

// stringLit returns the value of a plain string literal.
func stringLit(q *gojq.Query) (string, bool) {
	v, ok := literalOf(q)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// portableFlags reports whether flags are ones jq accepts and a backend can
// translate exactly. gojq accepts only i, m, g; of those we push i
// (case-insensitive) and m (jq's "dot matches newline", i.e. dotall), each
// backend mapping them to its own engine's spelling (see the mongo translator's
// mongoOptions). g only affects how many matches the test family collects, never
// test()'s boolean, so it is left to the client. s is not a jq flag at all (gojq
// rejects it at runtime), so it is never portable.
func portableFlags(flags string) bool {
	for _, c := range flags {
		if c != 'i' && c != 'm' {
			return false
		}
	}
	return true
}

// portableRegex reports whether a pattern uses only constructs that Oniguruma
// (jq's engine) and PCRE (MongoDB's) interpret identically, so pushing it cannot
// change which documents match. It is deliberately conservative: an unrecognized
// construct means "not portable", leaving the filter to the client-side pass.
// Rejected: group extensions `(?...)` (lookaround, named, inline flags, atomic),
// POSIX classes `[[:...:]]`, possessive quantifiers, backreferences, unicode
// property escapes, and any other engine-specific escape.
func portableRegex(p string) bool {
	i := 0
	for i < len(p) {
		switch c := p[i]; c {
		case '\\':
			// A backslash must be followed by a portable escape; the pair is
			// consumed together. Explicit advancement (rather than a loop-post
			// increment) keeps a bad index step a panic, not an infinite loop.
			if i+1 >= len(p) || !portableEscape(p[i+1]) {
				return false
			}
			i += 2
			continue
		case '(':
			if i+1 < len(p) && p[i+1] == '?' {
				return false
			}
		case '[':
			if i+1 < len(p) && p[i+1] == '[' {
				return false
			}
		case '*', '+', '?', '}':
			if i+1 < len(p) && p[i+1] == '+' {
				return false
			}
		}
		i++
	}
	return true
}

// portableEscape reports whether a backslash escape means the same, or a
// superset, across engines — pushing it must never drop a document jq keeps. The
// ASCII shorthand classes and word boundaries qualify, with one exception: \S is
// not portable. Go's RE2 \s omits the vertical tab (U+000B) that PCRE's \s
// includes, so RE2's \S matches a vertical tab PCRE's \S does not — pushing \S to
// a PCRE backend would narrow the match. \s diverges the other way (the pushed
// set is a superset), which the client-side re-run corrects, so it stays. An
// escaped punctuation character is a literal and qualifies; a digit
// (backreference) or any other letter (engine-specific, e.g. \p, \h, \A) does not.
func portableEscape(b byte) bool {
	switch b {
	case 'd', 'D', 'w', 'W', 's', 'b', 'B', 'n', 't', 'r', 'f', 'v':
		return true
	}
	if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') {
		return false
	}
	return true
}

// eqAtom builds an Eq from a `path == literal` comparison in either order.
func eqAtom(a, b *gojq.Query) (predicate.Node, bool) {
	path, v, _, ok := pathAndLiteral(a, b, literalOf)
	if !ok {
		return nil, false
	}
	return predicate.Eq{Path: path, Value: v}, true
}

// pathOf returns the field path of a plain relative index expression (`.a`,
// `.a.b`, `.["a"]`), or ok=false for anything with iteration, a computed index,
// or extra operators.
func pathOf(q *gojq.Query) ([]string, bool) {
	if q.Op != 0 || q.Term == nil || len(q.FuncDefs) != 0 {
		return nil, false
	}
	t := q.Term
	if t.Type != gojq.TermTypeIndex || t.Index == nil {
		return nil, false
	}
	name, ok := indexName(t.Index)
	if !ok {
		return nil, false
	}
	path := []string{name}
	for _, s := range t.SuffixList {
		if s.Iter || s.Index == nil {
			return nil, false
		}
		name, ok := indexName(s.Index)
		if !ok {
			return nil, false
		}
		path = append(path, name)
	}
	for _, c := range path {
		if !safeField(c) {
			return nil, false
		}
	}
	return path, true
}

// safeField reports whether a field name is safe to push as a backend path
// component. A name containing a dot is rejected because a backend that joins the
// path with dots (Mongo) would read it as a nested path and query the wrong
// field. A name beginning with '$' is rejected because it would land in operator
// position in a document-query backend — Mongo's `$where`/`$expr`, a CouchDB
// Mango selector key — turning an unsanitized field name into a server-side
// operator; the field is left to the client-side pass instead.
func safeField(name string) bool {
	return !strings.Contains(name, ".") && !strings.HasPrefix(name, "$")
}

// indexName returns the literal field name of an index (`.foo`, `."a.b"`,
// `.["foo"]`), or ok=false for a computed, slice, or interpolated index.
func indexName(idx *gojq.Index) (string, bool) {
	switch {
	case idx.Name != "":
		return idx.Name, true
	case idx.Str != nil && len(idx.Str.Queries) == 0:
		return idx.Str.Str, true
	case !idx.IsSlice && idx.End == nil && idx.Start != nil:
		return constString(idx.Start)
	default:
		return "", false
	}
}

// constString returns the value of a query that is exactly a plain string
// literal (how a bracket index like `.["foo"]` carries its key).
func constString(q *gojq.Query) (string, bool) {
	if q.Op != 0 || q.Term == nil || len(q.FuncDefs) != 0 {
		return "", false
	}
	t := q.Term
	if t.Type != gojq.TermTypeString || len(t.SuffixList) != 0 || t.Str == nil || len(t.Str.Queries) != 0 {
		return "", false
	}
	return t.Str.Str, true
}

// literalOf returns the JSON scalar value of a query that is exactly a number,
// string, boolean, or null literal, or ok=false otherwise.
func literalOf(q *gojq.Query) (any, bool) {
	if q.Op != 0 || q.Term == nil || len(q.FuncDefs) != 0 || len(q.Term.SuffixList) != 0 {
		return nil, false
	}
	t := q.Term
	switch t.Type {
	case gojq.TermTypeNumber:
		f, err := strconv.ParseFloat(t.Number, 64)
		if err != nil {
			return nil, false
		}
		return f, true
	case gojq.TermTypeString:
		if t.Str == nil || len(t.Str.Queries) != 0 {
			return nil, false
		}
		return t.Str.Str, true
	case gojq.TermTypeTrue:
		return true, true
	case gojq.TermTypeFalse:
		return false, true
	case gojq.TermTypeNull:
		return nil, true
	default:
		return nil, false
	}
}

// flattenAnd concatenates nodes into one And, absorbing any nested Ands.
func flattenAnd(nodes ...predicate.Node) predicate.And {
	var out predicate.And
	for _, n := range nodes {
		if a, ok := n.(predicate.And); ok {
			out = append(out, a...)
		} else {
			out = append(out, n)
		}
	}
	return out
}

// flattenOr concatenates nodes into one Or, absorbing any nested Ors.
func flattenOr(nodes ...predicate.Node) predicate.Or {
	var out predicate.Or
	for _, n := range nodes {
		if o, ok := n.(predicate.Or); ok {
			out = append(out, o...)
		} else {
			out = append(out, n)
		}
	}
	return out
}

// Package selector classifies a parsed jq expression by the reads it needs. The
// expression doubles as the key selector: its root-level paths name the store
// keys to fetch. When every root path resolves to a specific key, the fetch is
// bounded to O(keys requested). When it does not — a bare `.`, value iteration
// `.[]`, `keys`, a computed index, and so on — the expression can only be
// answered by materializing the whole keyspace, an unbounded read. This package
// decides which case an expression is; the caller decides whether an unbounded
// read is permitted.
package selector

import (
	"strings"

	"github.com/itchyny/gojq"
)

// KeySet is the outcome of classification. Scan is true when the expression
// needs the whole keyspace; then Keys is empty and the caller must scan.
// Otherwise Keys holds the referenced keys in first-seen order with duplicates
// removed. Streamable (meaningful only when Scan) is true when the expression is
// rooted at `.[]`, so it processes each value independently and can run over the
// keyspace in batches instead of materializing the whole dataset.
type KeySet struct {
	Scan       bool
	Streamable bool
	Keys       []string
}

// Keys walks q and classifies it. It never executes the expression; it only
// inspects the AST. A key is a leading index evaluated against the root, so the
// walk descends only into positions fed the root value and treats suffixes and
// the right side of a pipe as sub-navigation, not keys. Any construct that
// cannot be pinned to a specific key marks the whole expression as a scan.
func Keys(q *gojq.Query) KeySet {
	e := &extractor{seen: map[string]struct{}{}}
	e.visitQuery(q)
	if e.scan {
		return KeySet{Scan: true, Streamable: streamable(q)}
	}
	return KeySet{Keys: e.keys}
}

// streamable reports whether q is rooted at `.[]` — its top-level structure is
// value iteration over the root, optionally piped into a per-element residual
// (`.[]`, `.[] | select(...)`, `.[].name`). Such a filter distributes over any
// partition of the input, so it can be run batch by batch and its outputs
// concatenated. A filter that instead collapses the collection into one value
// (`.`, `keys`, `map(...)`, an aggregate, or a top-level comma) is not
// streamable and must be materialized.
func streamable(q *gojq.Query) bool {
	// A nil query names nothing to stream (Keys(nil) is a scan).
	if q == nil {
		return false
	}
	// Follow the leftmost stage of the pipe chain: `a | b | c` nests as
	// ((a | b) | c), so the first stage is the deepest Left.
	for q.Op == gojq.OpPipe {
		// `E as $x | body` is a pipe carrying binding patterns, and its body is fed
		// the *root*, not each bound element — so `.[] as $x | .` yields the whole
		// input once per element and does not distribute over a partition, however
		// much its left side looks like a plain `.[]`.
		if len(q.Patterns) > 0 {
			return false
		}
		q = q.Left
	}
	if q.Term == nil {
		// A non-pipe operator at the root (comma, arithmetic, …) does not
		// distribute over the input.
		return false
	}
	t := q.Term
	// The leading `.[]`: an identity whose first suffix is iteration. Trailing
	// suffixes (`.[].name`) stay per-element, so they remain streamable.
	return t.Type == gojq.TermTypeIdentity &&
		len(t.SuffixList) >= 1 &&
		t.SuffixList[0].Iter &&
		t.SuffixList[0].Index == nil
}

// extractor accumulates the referenced keys while walking the AST. Once scan is
// set the concrete keys no longer matter, but the walk simply stops adding them;
// there is nothing left to refuse.
type extractor struct {
	scan bool
	keys []string
	seen map[string]struct{}
}

// add records a referenced key, preserving first-seen order and dropping repeats
// so the fetch pipeline reads each key once.
func (e *extractor) add(key string) {
	if _, ok := e.seen[key]; ok {
		return
	}
	e.seen[key] = struct{}{}
	e.keys = append(e.keys, key)
}

// visitQuery walks a node whose input is the root value. A pipe feeds only its
// left side the root; the right side navigates the left's output, so its leading
// indices are not keys. Every other binary operator feeds both operands the root.
func (e *extractor) visitQuery(q *gojq.Query) {
	if q == nil {
		// gojq parses a source with no expression in it — empty, whitespace, or a
		// comment alone — into a query with no term and no operands. That filter
		// names no key, so it is a scan. gojq.Compile rejects it later with
		// "missing query", which is the message the user gets.
		e.scan = true
		return
	}
	if q.Term != nil {
		e.visitTerm(q.Term)
		return
	}
	if q.Op == gojq.OpPipe {
		e.visitQuery(q.Left)
		return
	}
	e.visitQuery(q.Left)
	e.visitQuery(q.Right)
}

// visitTerm walks a term whose input is the root value; its leading index, if
// any, is a key. Any construct that cannot be pinned to a specific key marks the
// expression as a scan. The suffix list is deliberately ignored: it navigates
// within the term's value, not the root.
func (e *extractor) visitTerm(t *gojq.Term) {
	switch t.Type {
	case gojq.TermTypeIdentity:
		// A bare `.` is the whole keyspace; `.` with any suffix (for example
		// `.[]`) iterates it. Both are scans.
		e.scan = true
	case gojq.TermTypeIndex:
		e.visitIndex(t.Index)
	case gojq.TermTypeQuery:
		e.visitQuery(t.Query)
	case gojq.TermTypeArray:
		if t.Array != nil && t.Array.Query != nil {
			e.visitQuery(t.Array.Query)
		}
	case gojq.TermTypeObject:
		e.visitObject(t.Object)
	case gojq.TermTypeUnary:
		e.visitTerm(t.Unary.Term)
	case gojq.TermTypeString:
		e.visitString(t.Str)
	case gojq.TermTypeNumber, gojq.TermTypeNull, gojq.TermTypeTrue,
		gojq.TermTypeFalse, gojq.TermTypeFormat:
		// Literals reference no key. An interpolated format string carries its
		// queries in Str, handled by TermTypeString above.
	default:
		// Func, Recurse (`..`), If, Try, Reduce, Foreach, Label, Break: none can
		// be pinned to a key, so they can only be answered by a scan.
		e.scan = true
	}
}

// visitIndex records the key named by a leading index. A bare field (`.foo`), a
// quoted field (`."a.b"`), or a bracketed string literal (`.["book:1"]`) resolves
// to a specific key. A slice, a numeric or computed subscript, or an interpolated
// string does not, and forces a scan. gojq stores the three literal forms in
// three different places: a bare field in Name, a quoted field in Str, and a
// bracketed subscript in Start (as a sub-query).
func (e *extractor) visitIndex(idx *gojq.Index) {
	switch {
	case idx.Name != "":
		e.add(idx.Name)
	case idx.Str != nil && len(idx.Str.Queries) == 0:
		e.add(idx.Str.Str)
	case !idx.IsSlice && idx.End == nil && idx.Start != nil:
		if key, ok := constString(idx.Start); ok {
			e.add(key)
			return
		}
		e.scan = true
	default:
		e.scan = true
	}
}

// constString reports the literal value of q when q is exactly a plain string
// literal with no interpolation, navigation, or operators; otherwise it reports
// false. It is how a bracketed key like `.["book:1"]` is recognised as specific.
func constString(q *gojq.Query) (string, bool) {
	if q.Term == nil || q.Op != 0 || len(q.FuncDefs) != 0 {
		return "", false
	}
	t := q.Term
	if t.Type != gojq.TermTypeString || len(t.SuffixList) != 0 {
		return "", false
	}
	if t.Str == nil || len(t.Str.Queries) != 0 {
		return "", false
	}
	return t.Str.Str, true
}

// visitObject walks an object constructor. Its values, and any computed keys,
// are evaluated against the root, so they are traversed for keys; a plain string
// key names a field of the result, not a store key. A shorthand entry (`{a}`,
// `{"foo"}`) carries no explicit value: its implied value is the same-named
// index, so the key name is itself a store key. The `{$x}` shorthand instead
// pulls from a variable and references no store key.
func (e *extractor) visitObject(o *gojq.Object) {
	for _, kv := range o.KeyVals {
		if kv.KeyQuery != nil {
			e.visitQuery(kv.KeyQuery)
		}
		if kv.Val != nil {
			e.visitQuery(kv.Val)
			continue
		}
		e.visitShorthand(kv)
	}
}

// visitShorthand records the store key implied by a valueless object entry. A
// bare identifier or plain string names the same-named key; a `$`-prefixed
// identifier is a variable, not a key; an interpolated string cannot be pinned
// and forces a scan.
func (e *extractor) visitShorthand(kv *gojq.ObjectKeyVal) {
	switch {
	case kv.KeyQuery != nil:
		// A computed key with no value has no implied index to resolve.
	case kv.Key != "":
		if strings.HasPrefix(kv.Key, "$") {
			return
		}
		e.add(kv.Key)
	case kv.KeyString != nil && len(kv.KeyString.Queries) == 0:
		e.add(kv.KeyString.Str)
	default:
		e.scan = true
	}
}

// visitString walks the queries embedded in an interpolated string; a plain
// string literal has none and references no key.
func (e *extractor) visitString(s *gojq.String) {
	for _, q := range s.Queries {
		e.visitQuery(q)
	}
}

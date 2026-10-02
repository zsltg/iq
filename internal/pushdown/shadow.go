package pushdown

import (
	"slices"
	"strconv"

	"github.com/itchyny/gojq"
)

// matchedBuiltins lists the gojq builtins, as name/arity, that the compiler
// recognizes by name. A user definition with one of these signatures replaces
// the builtin, so a predicate read from the call would not match what the
// query does.
var matchedBuiltins = map[string]struct{}{
	"select/1": {},
	"not/0":    {},
	"has/1":    {},
	"test/1":   {},
	"test/2":   {},
	"any/1":    {},
	"length/0": {},
}

// shadowsAny reports whether a definition anywhere in q replaces a builtin that
// the compiler matches. The walk covers the whole tree: every pipe node, operand,
// call argument and nested term. A definition in an unrelated scope also counts.
// This refuses a little more than needed, and it needs no scope tracking, so no
// extraction path can read a call that a definition replaces. A definition with
// another name or arity leaves the builtins alone.
func shadowsAny(q *gojq.Query) bool {
	if q == nil {
		return false
	}
	return shadowingDef(q.FuncDefs) || slices.ContainsFunc(queryChildren(q), shadowsAny)
}

// queryChildren returns the queries that q holds directly: its operands, its
// definition bodies, the computed keys of its patterns and the queries of its
// term. A child can be nil.
func queryChildren(q *gojq.Query) []*gojq.Query {
	qs := []*gojq.Query{q.Left, q.Right}
	for _, d := range q.FuncDefs {
		qs = append(qs, d.Body)
	}
	for _, p := range q.Patterns {
		qs = append(qs, patternQueries(p)...)
	}
	return append(qs, termQueries(q.Term)...)
}

// shadowingDef reports whether one of defs has the name and arity of a matched
// builtin.
func shadowingDef(defs []*gojq.FuncDef) bool {
	return slices.ContainsFunc(defs, func(d *gojq.FuncDef) bool {
		_, ok := matchedBuiltins[d.Name+"/"+strconv.Itoa(len(d.Args))]
		return ok
	})
}

// termQueries returns the queries that t holds, in any of its parts.
func termQueries(t *gojq.Term) []*gojq.Query {
	if t == nil {
		return nil
	}
	qs := append(containerQueries(t), flowQueries(t)...)
	return append(qs, accessQueries(t)...)
}

// containerQueries returns the queries of the call, object, array and if forms
// of t.
func containerQueries(t *gojq.Term) []*gojq.Query {
	var qs []*gojq.Query
	if t.Func != nil {
		qs = append(qs, t.Func.Args...)
	}
	qs = append(qs, objectQueries(t.Object)...)
	if t.Array != nil {
		qs = append(qs, t.Array.Query)
	}
	return append(qs, ifQueries(t.If)...)
}

// objectQueries returns the key and value queries of the object literal o.
func objectQueries(o *gojq.Object) []*gojq.Query {
	if o == nil {
		return nil
	}
	var qs []*gojq.Query
	for _, kv := range o.KeyVals {
		qs = append(qs, kv.KeyQuery, kv.Val)
		qs = append(qs, stringQueries(kv.KeyString)...)
	}
	return qs
}

// ifQueries returns the condition and branch queries of the if form f.
func ifQueries(f *gojq.If) []*gojq.Query {
	if f == nil {
		return nil
	}
	qs := []*gojq.Query{f.Cond, f.Then, f.Else}
	for _, e := range f.Elif {
		qs = append(qs, e.Cond, e.Then)
	}
	return qs
}

// flowQueries returns the queries of the try, reduce, foreach, label and
// parenthesis forms of t.
func flowQueries(t *gojq.Term) []*gojq.Query {
	var qs []*gojq.Query
	if t.Try != nil {
		qs = append(qs, t.Try.Body, t.Try.Catch)
	}
	if t.Reduce != nil {
		qs = append(qs, t.Reduce.Query, t.Reduce.Start, t.Reduce.Update)
		qs = append(qs, patternQueries(t.Reduce.Pattern)...)
	}
	if t.Foreach != nil {
		qs = append(qs, t.Foreach.Query, t.Foreach.Start, t.Foreach.Update, t.Foreach.Extract)
		qs = append(qs, patternQueries(t.Foreach.Pattern)...)
	}
	if t.Label != nil {
		qs = append(qs, t.Label.Body)
	}
	return append(qs, t.Query)
}

// accessQueries returns the queries of the string, index, suffix and unary parts
// of t.
func accessQueries(t *gojq.Term) []*gojq.Query {
	qs := stringQueries(t.Str)
	if t.Unary != nil {
		qs = append(qs, termQueries(t.Unary.Term)...)
	}
	qs = append(qs, indexQueries(t.Index)...)
	for _, s := range t.SuffixList {
		qs = append(qs, indexQueries(s.Index)...)
	}
	return qs
}

// stringQueries returns the interpolated queries of s.
func stringQueries(s *gojq.String) []*gojq.Query {
	if s == nil {
		return nil
	}
	return s.Queries
}

// indexQueries returns the key and slice bound queries of idx.
func indexQueries(idx *gojq.Index) []*gojq.Query {
	if idx == nil {
		return nil
	}
	return append([]*gojq.Query{idx.Start, idx.End}, stringQueries(idx.Str)...)
}

// patternQueries returns the queries inside a destructuring pattern, which are
// the computed keys of its object parts.
func patternQueries(p *gojq.Pattern) []*gojq.Query {
	if p == nil {
		return nil
	}
	var qs []*gojq.Query
	for _, e := range p.Array {
		qs = append(qs, patternQueries(e)...)
	}
	for _, o := range p.Object {
		qs = append(qs, o.KeyQuery)
		qs = append(qs, stringQueries(o.KeyString)...)
		qs = append(qs, patternQueries(o.Val)...)
	}
	return qs
}

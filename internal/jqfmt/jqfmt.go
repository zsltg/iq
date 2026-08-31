// Package jqfmt pretty-prints a jq filter: it parses the source with gojq and
// re-emits the AST with real line breaks, indentation, and optional syntax
// coloring. gojq itself only offers single-line canonical output ((*Query).String
// via an unexported writer), so the multi-line layout is done here.
//
// The printer controls layout only at structural break points — pipes, multi-entry
// objects, piped array bodies, if/try/reduce/foreach clauses, and breakable
// function arguments — and delegates every leaf token (indexes, strings, numbers,
// formats, patterns, suffixes) to gojq's own String(), so the emitted text always
// re-parses to the same query. The one deliberate deviation is a nested
// source("name"; "<sub>") call: the string sub-filter is unquoted and pretty-printed
// inline as its own indented block, which is the whole point of showing nested jq.
package jqfmt

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/itchyny/gojq"
)

// indentUnit is one level of indentation.
const indentUnit = "  "

// role names a syntactic category for coloring. Each maps to one ANSI code.
type role int

const (
	roleKeyword role = iota // if/then/reduce/as/def/and/or/…
	roleOp                  // | , == // + …
	rolePath                // .a.b, .["k"], .[]
	roleString              // "text"
	roleNumber              // 42, 1.5
	roleFunc                // function names
	roleLit                 // null / true / false
	roleFormat              // @base64
	rolePunc                // ( ) { } [ ] ; :
)

// ansi is the color code for each role, mirroring internal/render/json.go's style
// (dim punctuation so every reset stays paired). An empty string means no color.
var ansi = map[role]string{
	roleKeyword: "\x1b[35;1m",
	roleOp:      "\x1b[35m",
	rolePath:    "\x1b[34;1m",
	roleString:  "\x1b[32m",
	roleNumber:  "\x1b[36m",
	roleFunc:    "\x1b[33m",
	roleLit:     "\x1b[1m",
	roleFormat:  "\x1b[33;2m",
	rolePunc:    "\x1b[2m",
}

const ansiReset = "\x1b[0m"

// Format parses src as a jq filter and returns it pretty-printed, colored when
// colored is set. A parse failure is returned with context.
func Format(src string, colored bool) (string, error) {
	q, err := gojq.Parse(src)
	if err != nil {
		return "", fmt.Errorf("parse expression: %w", err)
	}
	return FormatQuery(q, colored), nil
}

// FormatQuery pretty-prints an already-parsed query. It is the recursive entry
// point, also used to render nested source() sub-filters.
func FormatQuery(q *gojq.Query, colored bool) string {
	p := &printer{colored: colored}
	p.query(q)
	return p.b.String()
}

// printer accumulates the formatted output at a current indent level.
type printer struct {
	b       strings.Builder
	colored bool
	indent  int
}

// tok writes text colored for its role (plain when coloring is off). Every color
// open is paired with a reset so the escape stream stays well-formed.
func (p *printer) tok(r role, text string) {
	if p.colored && ansi[r] != "" {
		p.b.WriteString(ansi[r])
		p.b.WriteString(text)
		p.b.WriteString(ansiReset)
		return
	}
	p.b.WriteString(text)
}

// nl writes a newline and the current indentation.
func (p *printer) nl() {
	p.b.WriteByte('\n')
	p.b.WriteString(strings.Repeat(indentUnit, p.indent))
}

// space writes a single separating space.
func (p *printer) space() { p.b.WriteByte(' ') }

// query prints a query: leading module/imports/func-defs, then either a single
// term or a `Left Op Right` composition.
func (p *printer) query(q *gojq.Query) {
	if q.Meta != nil {
		p.tok(roleKeyword, "module")
		p.space()
		p.b.WriteString(q.Meta.String())
		p.tok(rolePunc, ";")
		p.nl()
	}
	for _, im := range q.Imports {
		p.b.WriteString(strings.TrimRight(im.String(), "\n"))
		p.nl()
	}
	for _, fd := range q.FuncDefs {
		p.funcDef(fd)
		p.nl()
	}
	switch {
	case q.Term != nil:
		p.term(q.Term)
	case q.Right != nil:
		p.composed(q)
	}
}

// composed prints a `Left Op Right` query. A pipe (and the `as` binding that rides
// on it) breaks onto a new line so each stage reads on its own row; every other
// operator stays inline.
func (p *printer) composed(q *gojq.Query) {
	p.query(q.Left)
	if q.Op == gojq.OpPipe {
		for i, pat := range q.Patterns {
			p.space()
			if i == 0 {
				p.tok(roleKeyword, "as")
			} else {
				p.tok(roleOp, "?//")
			}
			p.space()
			p.tok(rolePath, pat.String())
		}
		p.nl()
		p.tok(roleOp, "|")
		p.space()
		p.query(q.Right)
		return
	}
	if q.Op == gojq.OpComma {
		p.tok(roleOp, ",")
		p.space()
		p.query(q.Right)
		return
	}
	p.space()
	p.tok(roleOp, q.Op.String())
	p.space()
	p.query(q.Right)
}

// funcDef prints `def name(args): body;`, breaking the body when it is complex.
func (p *printer) funcDef(fd *gojq.FuncDef) {
	p.tok(roleKeyword, "def")
	p.space()
	p.tok(roleFunc, fd.Name)
	if len(fd.Args) > 0 {
		p.tok(rolePunc, "(")
		p.b.WriteString(strings.Join(fd.Args, "; "))
		p.tok(rolePunc, ")")
	}
	p.tok(rolePunc, ":")
	p.space()
	p.query(fd.Body)
	p.tok(rolePunc, ";")
}

// term prints a single term. A leaf that needs no internal break is delegated to
// gojq's own String() (exact, re-parseable) and colored by its kind; a term with
// breakable structure is descended into so its layout and inner colors are
// controlled here.
func (p *printer) term(t *gojq.Term) {
	if src, ok := p.sourceCall(t); ok {
		p.b.WriteString(src)
		p.suffixes(t)
		return
	}
	if !termNeedsBreak(t) {
		p.leafTerm(t)
		return
	}
	switch t.Type {
	case gojq.TermTypeObject:
		p.object(t.Object)
	case gojq.TermTypeArray:
		p.array(t.Array)
	case gojq.TermTypeQuery:
		p.tok(rolePunc, "(")
		p.indent++
		p.nl()
		p.query(t.Query)
		p.indent--
		p.nl()
		p.tok(rolePunc, ")")
	case gojq.TermTypeFunc:
		p.funcCall(t.Func)
	case gojq.TermTypeUnary:
		p.tok(roleOp, t.Unary.Op.String())
		p.term(t.Unary.Term)
	case gojq.TermTypeIf:
		p.ifTerm(t.If)
	case gojq.TermTypeTry:
		p.tryTerm(t.Try)
	case gojq.TermTypeReduce:
		p.reduceTerm(t.Reduce)
	case gojq.TermTypeForeach:
		p.foreachTerm(t.Foreach)
	default:
		// No breakable layout for this kind; fall back to the exact form.
		p.leafTerm(t)
		return
	}
	p.suffixes(t)
}

// leafTerm writes a whole term via gojq's String(), colored by its kind. The
// suffix list is already included by String(), so callers must not re-emit it.
func (p *printer) leafTerm(t *gojq.Term) {
	switch t.Type {
	case gojq.TermTypeIdentity, gojq.TermTypeRecurse, gojq.TermTypeIndex:
		p.tok(rolePath, t.String())
	case gojq.TermTypeNumber:
		p.tok(roleNumber, t.String())
	case gojq.TermTypeString:
		p.tok(roleString, t.String())
	case gojq.TermTypeNull, gojq.TermTypeTrue, gojq.TermTypeFalse:
		p.tok(roleLit, t.String())
	case gojq.TermTypeFormat:
		p.tok(roleFormat, t.String())
	case gojq.TermTypeFunc:
		// A func with no breakable argument: name colored, args inline.
		p.funcCall(t.Func)
		p.suffixes(t)
	default:
		p.b.WriteString(t.String())
	}
}

// funcCall prints a function call: name, then a `(arg; arg)` list. Each argument
// is a query printed at one deeper indent when the call itself breaks.
func (p *printer) funcCall(f *gojq.Func) {
	p.tok(roleFunc, f.Name)
	if len(f.Args) == 0 {
		return
	}
	p.tok(rolePunc, "(")
	brk := anyQueryBreaks(f.Args)
	if brk {
		p.indent++
	}
	for i, a := range f.Args {
		if i > 0 {
			p.tok(rolePunc, ";")
			if !brk {
				p.space()
			}
		}
		if brk {
			p.nl()
		}
		p.query(a)
	}
	if brk {
		p.indent--
		p.nl()
	}
	p.tok(rolePunc, ")")
}

// object prints an object literal. An empty object stays `{}`; otherwise every
// entry is broken onto its own indented line.
func (p *printer) object(o *gojq.Object) {
	if len(o.KeyVals) == 0 {
		p.tok(rolePunc, "{}")
		return
	}
	p.tok(rolePunc, "{")
	p.indent++
	for i, kv := range o.KeyVals {
		if i > 0 {
			p.tok(rolePunc, ",")
		}
		p.nl()
		p.objectKey(kv)
		if kv.Val != nil {
			p.tok(rolePunc, ":")
			p.space()
			p.query(kv.Val)
		}
	}
	p.indent--
	p.nl()
	p.tok(rolePunc, "}")
}

// objectKey prints an object entry's key: a bare identifier, a quoted string, or a
// parenthesized computed key.
func (p *printer) objectKey(kv *gojq.ObjectKeyVal) {
	switch {
	case kv.Key != "":
		if strings.HasPrefix(kv.Key, "$") {
			p.tok(rolePath, kv.Key)
		} else {
			p.tok(roleFunc, kv.Key)
		}
	case kv.KeyString != nil:
		p.tok(roleString, kv.KeyString.String())
	case kv.KeyQuery != nil:
		p.tok(rolePunc, "(")
		p.query(kv.KeyQuery)
		p.tok(rolePunc, ")")
	}
}

// array prints an array literal, breaking the body onto indented lines when it
// contains a pipe or other breakable structure.
func (p *printer) array(a *gojq.Array) {
	if a.Query == nil {
		p.tok(rolePunc, "[]")
		return
	}
	p.tok(rolePunc, "[")
	p.indent++
	p.nl()
	p.query(a.Query)
	p.indent--
	p.nl()
	p.tok(rolePunc, "]")
}

// ifTerm prints an if/elif/else/end, one clause per line at the current indent.
func (p *printer) ifTerm(e *gojq.If) {
	p.tok(roleKeyword, "if")
	p.space()
	p.query(e.Cond)
	p.nl()
	p.tok(roleKeyword, "then")
	p.clauseBody(e.Then)
	for _, elif := range e.Elif {
		p.nl()
		p.tok(roleKeyword, "elif")
		p.space()
		p.query(elif.Cond)
		p.nl()
		p.tok(roleKeyword, "then")
		p.clauseBody(elif.Then)
	}
	if e.Else != nil {
		p.nl()
		p.tok(roleKeyword, "else")
		p.clauseBody(e.Else)
	}
	p.nl()
	p.tok(roleKeyword, "end")
}

// tryTerm prints try/catch.
func (p *printer) tryTerm(e *gojq.Try) {
	p.tok(roleKeyword, "try")
	p.clauseBody(e.Body)
	if e.Catch != nil {
		p.nl()
		p.tok(roleKeyword, "catch")
		p.clauseBody(e.Catch)
	}
}

// reduceTerm prints `reduce src as pat (init; update)`.
func (p *printer) reduceTerm(e *gojq.Reduce) {
	p.accumulator("reduce", e.Query, e.Pattern, e.Start, e.Update)
}

// foreachTerm prints `foreach src as pat (init; update[; extract])`.
func (p *printer) foreachTerm(e *gojq.Foreach) {
	clauses := []*gojq.Query{e.Start, e.Update}
	if e.Extract != nil {
		clauses = append(clauses, e.Extract)
	}
	p.accumulator("foreach", e.Query, e.Pattern, clauses...)
}

// accumulator prints the shared `<kw> src as pat (clause; clause[; clause])` shape
// of reduce and foreach, with the parenthesized clauses broken onto indented lines
// when any of them is itself multi-line.
func (p *printer) accumulator(kw string, src *gojq.Query, pat *gojq.Pattern, clauses ...*gojq.Query) {
	p.tok(roleKeyword, kw)
	p.space()
	p.query(src)
	p.space()
	p.tok(roleKeyword, "as")
	p.space()
	p.tok(rolePath, pat.String())
	p.space()
	p.parenClauses(clauses...)
}

// parenClauses prints `(a; b; …)`, keeping the clauses inline unless one breaks,
// in which case each clause goes on its own indented line.
func (p *printer) parenClauses(clauses ...*gojq.Query) {
	brk := anyQueryBreaks(clauses)
	p.tok(rolePunc, "(")
	if brk {
		p.indent++
	}
	for i, q := range clauses {
		if i > 0 {
			p.tok(rolePunc, ";")
			if !brk {
				p.space()
			}
		}
		if brk {
			p.nl()
		}
		p.query(q)
	}
	if brk {
		p.indent--
		p.nl()
	}
	p.tok(rolePunc, ")")
}

// clauseBody prints a keyword's body: a simple body follows on the same line after
// a space; a body that breaks moves to its own indented line, so no trailing space
// is left on the keyword line.
func (p *printer) clauseBody(q *gojq.Query) {
	if !queryNeedsBreak(q) {
		p.space()
		p.query(q)
		return
	}
	p.indent++
	p.nl()
	p.query(q)
	p.indent--
}

// suffixes appends a term's suffix list (`.a`, `[]`, `[k]`, `?`) after a descended
// base. Each suffix is delegated to gojq for exact text; adjacency after `)`/`]`/`}`
// never needs the space gojq inserts only after `.` or a digit.
func (p *printer) suffixes(t *gojq.Term) {
	for _, s := range t.SuffixList {
		p.tok(rolePath, s.String())
	}
}

// sourceCall renders a `source("name"; "<sub>")` call with the string sub-filter
// unquoted and pretty-printed inline as an indented block, returning ok=true when t
// is exactly that shape. A one-argument source("name") or a non-literal sub-filter
// is left to the ordinary path.
func (p *printer) sourceCall(t *gojq.Term) (string, bool) {
	if t.Type != gojq.TermTypeFunc || t.Func == nil || t.Func.Name != "source" {
		return "", false
	}
	if len(t.Func.Args) != 2 {
		return "", false
	}
	if _, ok := stringLiteral(t.Func.Args[0]); !ok {
		return "", false
	}
	sub, ok := stringLiteral(t.Func.Args[1])
	if !ok {
		return "", false
	}
	inner, err := gojq.Parse(sub)
	if err != nil {
		return "", false
	}
	sub = FormatQuery(inner, p.colored)
	// Re-indent the nested block under the source() call.
	pad := strings.Repeat(indentUnit, p.indent+1)
	sub = pad + strings.ReplaceAll(sub, "\n", "\n"+pad)

	var q printer
	q.colored = p.colored
	q.tok(roleFunc, "source")
	q.tok(rolePunc, "(")
	q.tok(roleString, t.Func.Args[0].String())
	q.tok(rolePunc, ";")
	q.b.WriteString("\n")
	q.b.WriteString(sub)
	q.b.WriteString("\n" + strings.Repeat(indentUnit, p.indent))
	q.tok(rolePunc, ")")
	return q.b.String(), true
}

// stringLiteral returns the value of a query that is exactly a plain (non-
// interpolated) string literal, the form a static source() argument takes.
func stringLiteral(q *gojq.Query) (string, bool) {
	if q.Op != 0 || q.Term == nil || len(q.FuncDefs) != 0 {
		return "", false
	}
	t := q.Term
	if t.Type != gojq.TermTypeString || len(t.SuffixList) != 0 || t.Str == nil || t.Str.Queries != nil {
		return "", false
	}
	return t.Str.Str, true
}

// termNeedsBreak reports whether a term has internal structure worth breaking onto
// multiple lines. Leaves (indexes, scalars, formats) never break; composites break
// when they contain a pipe, a multi-entry/complex object, a piped array, a
// breakable control form, or a function argument that itself breaks.
func termNeedsBreak(t *gojq.Term) bool {
	switch t.Type {
	case gojq.TermTypeObject:
		return objectNeedsBreak(t.Object)
	case gojq.TermTypeArray:
		return t.Array.Query != nil && queryNeedsBreak(t.Array.Query)
	case gojq.TermTypeQuery:
		return queryNeedsBreak(t.Query)
	case gojq.TermTypeFunc:
		return anyQueryBreaks(t.Func.Args)
	case gojq.TermTypeUnary:
		return termNeedsBreak(t.Unary.Term)
	case gojq.TermTypeIf:
		// An elif or an else clause breaks the form on its own, whatever those clauses
		// hold, so their contents never need asking: only a bare if/then can stay inline,
		// and only when neither of its two parts breaks.
		if len(t.If.Elif) > 0 || t.If.Else != nil {
			return true
		}
		return queryNeedsBreak(t.If.Cond) || queryNeedsBreak(t.If.Then)
	case gojq.TermTypeTry:
		return queryNeedsBreak(t.Try.Body) || (t.Try.Catch != nil && queryNeedsBreak(t.Try.Catch))
	case gojq.TermTypeReduce:
		return queryNeedsBreak(t.Reduce.Start) || queryNeedsBreak(t.Reduce.Update)
	case gojq.TermTypeForeach:
		return queryNeedsBreak(t.Foreach.Start) || queryNeedsBreak(t.Foreach.Update) ||
			(t.Foreach.Extract != nil && queryNeedsBreak(t.Foreach.Extract))
	default:
		return false
	}
}

// queryNeedsBreak reports whether a query would span multiple lines: a pipe always
// breaks, func-defs break, and a binary/comma composition breaks when either side
// does.
func queryNeedsBreak(q *gojq.Query) bool {
	if len(q.FuncDefs) > 0 || q.Meta != nil {
		return true
	}
	if q.Term != nil {
		return termNeedsBreak(q.Term)
	}
	if q.Right == nil {
		return false
	}
	if q.Op == gojq.OpPipe {
		return true
	}
	return queryNeedsBreak(q.Left) || queryNeedsBreak(q.Right)
}

// objectNeedsBreak reports whether an object literal should break: more than one
// entry, or any value/computed-key that itself breaks.
func objectNeedsBreak(o *gojq.Object) bool {
	if len(o.KeyVals) > 1 {
		return true
	}
	for _, kv := range o.KeyVals {
		if kv.Val != nil && queryNeedsBreak(kv.Val) {
			return true
		}
		if kv.KeyQuery != nil && queryNeedsBreak(kv.KeyQuery) {
			return true
		}
	}
	return false
}

// anyQueryBreaks reports whether any query in args needs breaking.
func anyQueryBreaks(args []*gojq.Query) bool {
	return slices.ContainsFunc(args, queryNeedsBreak)
}

// Stage is one top-level pipe stage of an explained filter: its pretty-printed
// Text (possibly multi-line, colored per the Explain call) and a short human Desc.
// Desc is empty when no confident description applies — a wrong note is worse than
// none.
type Stage struct {
	Text string
	Desc string
}

// HasLeadingDecls reports whether q begins with module/import/func-def declarations,
// which ExplainQuery peels into its own stage 0. The data-access mark uses it to know
// the root body stage is index 1 rather than 0, so both share one definition.
func HasLeadingDecls(q *gojq.Query) bool {
	return q.Meta != nil || len(q.Imports) > 0 || len(q.FuncDefs) > 0
}

// ExplainQuery breaks an already-parsed query's top-level pipe chain into ordered
// stages, left-most first, each with its pretty-printed Text and a short Desc.
// Leading module/imports/func-defs form their own leading stage. The caller parses
// once and reuses q, so no parse (and no parse error) happens here.
func ExplainQuery(q *gojq.Query, colored bool) []Stage {
	var stages []Stage
	// Leading declarations (module/imports/func-defs) ride on the top query; peel
	// them into their own stage so the pipe split below sees only the body. The body
	// is the whole query with just those declaration fields cleared, so every other
	// field (term, pipe operands, patterns, op) carries over untouched.
	if HasLeadingDecls(q) {
		decls := &gojq.Query{Meta: q.Meta, Imports: q.Imports, FuncDefs: q.FuncDefs}
		stages = append(stages, Stage{Text: strings.TrimRight(FormatQuery(decls, colored), "\n")})
		body := *q
		body.Meta, body.Imports, body.FuncDefs = nil, nil, nil
		q = &body
	}
	for _, s := range splitPipes(q) {
		stages = append(stages, Stage{Text: stageText(s.q, s.pats, colored), Desc: describe(s.q)})
	}
	return stages
}

// pipeStage is one operand of the top-level pipe chain, carrying the `as $pat`
// binding that rides on the pipe query and renders after this operand.
type pipeStage struct {
	q    *gojq.Query
	pats []*gojq.Pattern
}

// splitPipes unwinds a left-leaning top-level pipe chain into ordered operands,
// left-most first. Each pipe contributes its Left operand plus the patterns that
// ride on it; the final non-pipe query is the last stage.
func splitPipes(q *gojq.Query) []pipeStage {
	var out []pipeStage
	// A parsed OpPipe query always carries a Right operand, so the loop needs no nil
	// guard: every pipe contributes its Left, and the final non-pipe query is the tail.
	for {
		if q.Op == gojq.OpPipe {
			out = append(out, pipeStage{q: q.Left, pats: q.Patterns})
			q = q.Right
			continue
		}
		out = append(out, pipeStage{q: q})
		return out
	}
}

// stageText pretty-prints one stage, appending its `as $pat` binding exactly as
// composed renders it (`<left> as <pat>`), so the binding stays with its operand.
func stageText(q *gojq.Query, pats []*gojq.Pattern, colored bool) string {
	// With no patterns this is exactly FormatQuery(q); the loop below simply adds
	// nothing, so there is no separate fast path to keep in sync.
	p := &printer{colored: colored}
	p.query(q)
	for i, pat := range pats {
		p.space()
		if i == 0 {
			p.tok(roleKeyword, "as")
		} else {
			p.tok(roleOp, "?//")
		}
		p.space()
		p.tok(rolePath, pat.String())
	}
	return p.b.String()
}

// describe returns a short lowercase description of a stage, or "" when the shape
// is not confidently recognized. A composition describes by its operator; a single
// term dispatches on its kind.
func describe(q *gojq.Query) string {
	// A stage is either a single term or a top-level composition (Term nil): the
	// composition is described by its operator, the term by its kind.
	if q.Term == nil {
		return describeOp(q.Op)
	}
	return describeTerm(q.Term)
}

// describeOp names a top-level binary composition by its operator.
func describeOp(op gojq.Operator) string {
	switch op {
	case gojq.OpComma:
		return "emit multiple values"
	case gojq.OpEq, gojq.OpNe, gojq.OpGt, gojq.OpLt, gojq.OpGe, gojq.OpLe:
		return "compare with " + op.String()
	case gojq.OpAdd, gojq.OpSub, gojq.OpMul, gojq.OpDiv, gojq.OpMod:
		return "compute with " + op.String()
	case gojq.OpAlt:
		return "default with //"
	default:
		return ""
	}
}

// describeTerm dispatches a single term to its description, or "" when the kind
// carries no confident note.
func describeTerm(t *gojq.Term) string {
	switch t.Type {
	case gojq.TermTypeIdentity, gojq.TermTypeIndex, gojq.TermTypeRecurse:
		return describePath(t)
	case gojq.TermTypeFunc:
		return describeFunc(t.Func)
	case gojq.TermTypeObject:
		return describeObject(t.Object)
	case gojq.TermTypeArray:
		return "collect into an array"
	case gojq.TermTypeIf:
		return "conditional"
	case gojq.TermTypeReduce:
		return "accumulate over " + inlineArg(t.Reduce.Query)
	case gojq.TermTypeForeach:
		return "accumulate over " + inlineArg(t.Foreach.Query)
	case gojq.TermTypeFormat:
		return "apply " + t.Format
	case gojq.TermTypeQuery:
		return describe(t.Query)
	default:
		return ""
	}
}

// describePath describes an identity/index/recurse path by its rendered form: the
// whole input, an element iteration, or a named field.
func describePath(t *gojq.Term) string {
	s := t.String()
	switch {
	case s == ".":
		return "the whole input"
	case s == "..":
		return "recurse over all values"
	case s == ".[]":
		return "each element"
	case strings.HasSuffix(s, "[]"):
		return "each element of " + strings.TrimSuffix(s, "[]")
	default:
		return "field " + s
	}
}

// describeObject describes an object construction, appending the key list when
// every key is a cheap identifier or plain string (a computed key drops the list).
func describeObject(o *gojq.Object) string {
	var keys []string
	for _, kv := range o.KeyVals {
		switch {
		case kv.Key != "":
			keys = append(keys, strings.TrimPrefix(kv.Key, "$"))
		case kv.KeyString != nil && kv.KeyString.Queries == nil:
			keys = append(keys, kv.KeyString.Str)
		default:
			return "build an object"
		}
	}
	if len(keys) == 0 {
		return "build an object"
	}
	return "build an object (" + strings.Join(keys, ", ") + ")"
}

// builtinDesc maps a jq builtin to a static description. Builtins whose note needs
// an argument rendered are handled in describeFunc before this table is consulted.
var builtinDesc = map[string]string{
	"length":         "length",
	"keys":           "sorted keys",
	"keys_unsorted":  "keys",
	"add":            "sum / concatenate",
	"sort":           "sort",
	"unique":         "unique values",
	"reverse":        "reverse",
	"flatten":        "flatten nested arrays",
	"to_entries":     "to key/value pairs",
	"from_entries":   "from key/value pairs",
	"type":           "the value's type",
	"tonumber":       "parse as number",
	"tostring":       "convert to string",
	"ascii_downcase": "lowercase",
	"ascii_upcase":   "uppercase",
	"first":          "first element",
	"last":           "last element",
}

// describeFunc describes a function call: the argument-taking builtins first (their
// note renders the argument inline), then the static table, then a safe "call
// <name>" fallback that is never misleading.
func describeFunc(f *gojq.Func) string {
	switch f.Name {
	case "select":
		if len(f.Args) == 1 {
			return "keep inputs where " + inlineArg(f.Args[0])
		}
	case "map":
		if len(f.Args) == 1 {
			return "apply " + inlineArg(f.Args[0]) + " to each element"
		}
	case "map_values":
		if len(f.Args) == 1 {
			return "map each value with " + inlineArg(f.Args[0])
		}
	case "sort_by":
		if len(f.Args) == 1 {
			return "sort by " + inlineArg(f.Args[0])
		}
	case "group_by":
		if len(f.Args) == 1 {
			return "group by " + inlineArg(f.Args[0])
		}
	case "unique_by":
		if len(f.Args) == 1 {
			return "unique by " + inlineArg(f.Args[0])
		}
	case "min_by":
		if len(f.Args) == 1 {
			return "minimum by " + inlineArg(f.Args[0])
		}
	case "max_by":
		if len(f.Args) == 1 {
			return "maximum by " + inlineArg(f.Args[0])
		}
	case "has":
		if len(f.Args) == 1 {
			return "has key " + inlineArg(f.Args[0])
		}
	case "contains":
		if len(f.Args) == 1 {
			return "contains " + inlineArg(f.Args[0])
		}
	case "del":
		if len(f.Args) == 1 {
			return "delete " + inlineArg(f.Args[0])
		}
	case "limit":
		if len(f.Args) == 2 {
			return "first " + inlineArg(f.Args[0]) + " of " + inlineArg(f.Args[1])
		}
	case "split":
		if len(f.Args) >= 1 {
			return "split on " + inlineArg(f.Args[0])
		}
	case "join":
		if len(f.Args) == 1 {
			return "join with " + inlineArg(f.Args[0])
		}
	case "test":
		if len(f.Args) >= 1 {
			return "regex test " + inlineArg(f.Args[0])
		}
	case "match":
		if len(f.Args) >= 1 {
			return "regex match " + inlineArg(f.Args[0])
		}
	case "startswith":
		if len(f.Args) == 1 {
			return "starts with " + inlineArg(f.Args[0])
		}
	case "endswith":
		if len(f.Args) == 1 {
			return "ends with " + inlineArg(f.Args[0])
		}
	case "ltrimstr":
		if len(f.Args) == 1 {
			return "trim prefix " + inlineArg(f.Args[0])
		}
	case "rtrimstr":
		if len(f.Args) == 1 {
			return "trim suffix " + inlineArg(f.Args[0])
		}
	}
	if d, ok := builtinDesc[f.Name]; ok {
		return d
	}
	return "call " + f.Name
}

// inlineArg renders a query argument on a single line, collapsing the pretty
// printer's line breaks so a description stays on one row.
func inlineArg(q *gojq.Query) string {
	return strings.Join(strings.Fields(FormatQuery(q, false)), " ")
}

// sgrEscape matches a complete ANSI SGR escape (`\x1b[…m`), the only escape this
// package emits. A truncated or malformed sequence does not match, so its bytes are
// counted like any other text.
var sgrEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

// VisibleWidth returns the display width of s ignoring this package's ANSI SGR escape
// runs: it strips every complete `\x1b[…m` sequence, then counts the remaining runes,
// so a colored stage aligns against a plain one.
func VisibleWidth(s string) int {
	return utf8.RuneCountInString(sgrEscape.ReplaceAllString(s, ""))
}

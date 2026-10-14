package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zsltg/iq/internal/jqfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/pushdown"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// noteColumn is the width of the left text of a row, after the section indent.
// The note of the row starts after it.
const noteColumn = 34

// sectionIndent is the indent of a top-level row. The width of the left text counts
// from there.
const sectionIndent = 2

// conjunctContext is what the planner of one conjunct needs besides the conjunct:
// the driver, the source, the keys, and the access plan of the whole predicate.
type conjunctContext struct {
	driver driver
	src    planSource
	keys   selector.KeySet
	plan   query.AccessPlan
}

// conjunctStages plans the stages of one conjunct on a streaming route. A conjunct
// that did not compile is for jq only. A compiled conjunct goes to the server if the
// translator of the driver gives it a fragment, to the prefilter if that stage runs,
// and always to jq. The stage states of the whole predicate (cx.plan) decide which
// stages the plan names. The plan never names a stage whose state is none.
func conjunctStages(cx conjunctContext, cj pushdown.Conjunct) (conjunctPlan, error) {
	if cj.Pred == nil {
		return conjunctPlan{Expr: cj.Expr, Stages: []string{stageJQ}, Reason: "not compiled: " + cj.Reason}, nil
	}
	c := conjunctPlan{Expr: cj.Expr, Compiled: cj.Pred.String(), node: cj.Pred}
	if err := c.planServer(cx, cj.Pred); err != nil {
		return conjunctPlan{}, err
	}
	c.planPrefilter(cx.plan.Prefilter)
	c.Stages = append(c.Stages, stageJQ)
	return c, nil
}

// planServer adds the server stage if the driver gives the conjunct a fragment. It
// records the skip if the driver declines. It does nothing if the server stage is
// none.
func (c *conjunctPlan) planServer(cx conjunctContext, pred predicate.Node) error {
	if cx.plan.Server.State == query.StageNone {
		return nil
	}
	// Only a streaming read has conjunct plans, so the read is never unbounded here.
	cp, err := cx.driver.explainPlan(cx.src, cx.keys, pred, false)
	if err != nil {
		return fmt.Errorf("plan %s conjunct: %w", cx.driver.name, err)
	}
	if cp.Filter == nil {
		c.skip(stageServer, declinedReason)
		return nil
	}
	c.Server = cp.Filter
	c.Stages = append(c.Stages, stageServer)
	return nil
}

// planPrefilter adds the prefilter stage, or records why it is off.
func (c *conjunctPlan) planPrefilter(st query.StagePlan) {
	switch st.State {
	case query.StageOn, query.StageConditional:
		c.Stages = append(c.Stages, stagePrefilter)
	case query.StageOff:
		c.skip(stagePrefilter, st.Note)
	}
}

// skip records why a stage does not evaluate the conjunct.
func (c *conjunctPlan) skip(stage, reason string) {
	if c.Skipped == nil {
		c.Skipped = map[string]string{}
	}
	c.Skipped[stage] = reason
}

// has reports whether the conjunct is planned for the stage.
func (c conjunctPlan) has(stage string) bool {
	return slices.Contains(c.Stages, stage)
}

// jqOnly reports whether no stage before jq evaluates the conjunct.
func (c conjunctPlan) jqOnly() bool {
	return len(c.Stages) == 1
}

// writeStageSections appends the server filter, prefilter, and jq sections. A plan
// with no conjuncts writes only the key fetch filter of a bounded read, if any. The
// materialized route writes the jq section alone.
func writeStageSections(b *strings.Builder, sp sourcePlan) error {
	if len(sp.conjuncts) == 0 {
		return writeKeyFetchFilter(b, sp)
	}
	if !sp.materialized {
		if err := writeServerSection(b, sp); err != nil {
			return err
		}
		writePrefilterSection(b, sp)
	}
	writeJQSection(b, sp)
	return nil
}

// writeKeyFetchFilter appends the server filter of a plan with no conjunct rows,
// for example the id list of a keyed read. It uses the layout of a fragment row.
func writeKeyFetchFilter(b *strings.Builder, sp sourcePlan) error {
	if sp.filter == nil {
		return nil
	}
	lines, err := fitJSON(sp.filter, sectionIndent, noteColumn+sectionIndent)
	if err != nil {
		return fmt.Errorf("render server filter: %w", err)
	}
	label := "server filter"
	if !sp.keys.Scan {
		label += " (key fetch)"
	}
	writePlanSection(b, label)
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	return nil
}

// writeStageHeader writes the header of a stage that has rows, or its one-line form
// for a stage that is none or off. It returns true if rows follow. name is the stage
// label, and header is the text of the section header for an on or conditional stage.
func writeStageHeader(b *strings.Builder, name string, st query.StagePlan, header string) bool {
	switch st.State {
	case query.StageNone, query.StageOff:
		b.WriteString("\n")
		writePlanLine(b, name, st.State.String()+", "+st.Note)
		return false
	default:
		writePlanSection(b, header)
		return true
	}
}

// writeServerSection appends the server filter section. Each row is the server
// fragment of one conjunct.
func writeServerSection(b *strings.Builder, sp sourcePlan) error {
	st := sp.server
	header := fmt.Sprintf("server filter (%s)", st.Scope)
	if st.State == query.StageConditional {
		header = fmt.Sprintf("server filter (%s, if %s)", st.Scope, st.Note)
	}
	if !writeStageHeader(b, "server filter", st, header) {
		return nil
	}
	for _, c := range sp.conjuncts {
		if c.Server == nil {
			continue
		}
		if err := writeFragmentRow(b, c.Server, c.Expr); err != nil {
			return err
		}
	}
	return nil
}

// writeFragmentRow writes one server fragment with the jq source of its conjunct as
// the note. A fragment that fits the left column prints on one line. A wider one
// breaks as fitJSON says, with the note on its first line.
func writeFragmentRow(b *strings.Builder, frag map[string]any, expr string) error {
	lines, err := fitJSON(frag, sectionIndent, noteColumn+sectionIndent)
	if err != nil {
		return fmt.Errorf("render server fragment: %w", err)
	}
	writeNoteRow(b, sectionIndent, lines[0][sectionIndent:], "— "+expr)
	for _, l := range lines[1:] {
		b.WriteString(l + "\n")
	}
	return nil
}

// maxJSONWidth is the line width in which a member of a broken fragment stays on one
// line.
const maxJSONWidth = 80

// fitJSON lays out a JSON value as lines, each with the given indent. The value is
// one line in compact form (": " after a key, ", " between elements, keys sorted,
// no HTML escaping) if the line fits in width. Else an object or array opens on the
// first line, each member goes on its own line two spaces deeper, and the closing
// bracket goes on its own line. Each member fits in maxJSONWidth or breaks the same
// way. A scalar never breaks. It fails for a value that JSON cannot encode.
func fitJSON(v any, indent, width int) ([]string, error) {
	data, err := json.Marshal(v)
	var norm any
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		err = dec.Decode(&norm)
	}
	if err != nil {
		return nil, fmt.Errorf("encode JSON: %w", err)
	}
	return layoutJSON(norm, jsonSlot{indent: indent, width: width}), nil
}

// jsonSlot is where a value sits in the layout: the indent and the width of its line,
// the key text before it (the prefix), and the comma after it (the suffix).
type jsonSlot struct {
	indent int
	width  int
	prefix string
	suffix string
}

// line is the one-line form of a value in the slot.
func (s jsonSlot) line(compact string) string {
	return strings.Repeat(" ", s.indent) + s.prefix + compact + s.suffix
}

// fits reports whether the one-line form of a value fits the width of the slot.
func (s jsonSlot) fits(compact string) bool {
	return s.indent+utf8.RuneCountInString(s.prefix+compact+s.suffix) <= s.width
}

// jsonMember is one member of an object or array: the key text before its value
// (empty for an array) and the value.
type jsonMember struct {
	label string
	value any
}

// jsonContainer is an object or array: its brackets and its members in order.
type jsonContainer struct {
	open, closer string
	members      []jsonMember
}

// containerOf returns the container form of a normalized value. It returns false for
// a scalar.
func containerOf(v any) (jsonContainer, bool) {
	switch t := v.(type) {
	case map[string]any:
		return jsonContainer{open: "{", closer: "}", members: objectMembers(t)}, true
	case []any:
		members := make([]jsonMember, 0, len(t))
		for _, e := range t {
			members = append(members, jsonMember{value: e})
		}
		return jsonContainer{open: "[", closer: "]", members: members}, true
	}
	return jsonContainer{}, false
}

// objectMembers lists the members of an object with sorted keys.
func objectMembers(m map[string]any) []jsonMember {
	members := make([]jsonMember, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		members = append(members, jsonMember{label: jsonString(k) + ": ", value: m[k]})
	}
	return members
}

// layoutJSON lays out a normalized value (maps, slices, json.Number, string, bool,
// nil) in a slot.
func layoutJSON(v any, s jsonSlot) []string {
	compact := compactJSON(v)
	if s.fits(compact) {
		return []string{s.line(compact)}
	}
	c, ok := containerOf(v)
	if !ok {
		return []string{s.line(compact)}
	}
	return layoutContainer(c, s)
}

// layoutContainer breaks a container over lines. The opening bracket goes on the
// first line, each member on its own line two spaces deeper, and the closing bracket
// on its own line.
func layoutContainer(c jsonContainer, s jsonSlot) []string {
	pad := strings.Repeat(" ", s.indent)
	lines := []string{pad + s.prefix + c.open}
	for i, m := range c.members {
		sep := ","
		if i == len(c.members)-1 {
			sep = ""
		}
		slot := jsonSlot{indent: s.indent + 2, width: maxJSONWidth, prefix: m.label, suffix: sep}
		lines = append(lines, layoutJSON(m.value, slot)...)
	}
	return append(lines, pad+c.closer+s.suffix)
}

// compactJSON is the one-line form of a normalized value.
func compactJSON(v any) string {
	switch t := v.(type) {
	case map[string]any:
		return compactObject(t)
	case []any:
		return compactArray(t)
	case string:
		return jsonString(t)
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	default:
		return "null"
	}
}

// compactObject is the one-line form of an object, with sorted keys.
func compactObject(m map[string]any) string {
	parts := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		parts = append(parts, jsonString(k)+": "+compactJSON(m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// compactArray is the one-line form of an array.
func compactArray(a []any) string {
	parts := make([]string, 0, len(a))
	for _, e := range a {
		parts = append(parts, compactJSON(e))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// jsonEscapes are the runes that a JSON string writes as an escape. The short forms
// and the two line separators match encoding/json.
var jsonEscapes = map[rune]string{
	'"':      `\"`,
	'\\':     `\\`,
	'\n':     `\n`,
	'\r':     `\r`,
	'\t':     `\t`,
	'\b':     `\b`,
	'\f':     `\f`,
	'\u2028': `\u2028`,
	'\u2029': `\u2029`,
}

// jsonString is a JSON string literal with no HTML escaping. The text comes from a
// decoded JSON value, so it is valid UTF-8. A control rune below 0x20 with no short
// form is written as \u00XX.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if esc, ok := jsonEscapes[r]; ok {
			b.WriteString(esc)
		} else if r < 0x20 {
			b.WriteString(`\u` + fmt.Sprintf("%04x", r))
		} else {
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// writePrefilterSection appends the prefilter section. Each row is a compiled
// conjunct. Two or more conjuncts nest under an `and` line.
func writePrefilterSection(b *strings.Builder, sp sourcePlan) {
	st := sp.prefilter
	if st.State == query.StageNone {
		return
	}
	if !writeStageHeader(b, "prefilter", st, prefilterHeader(st)) {
		return
	}
	writePrefilterRows(b, sp, conjunctsWhere(sp.conjuncts, func(c conjunctPlan) bool {
		return c.has(stagePrefilter)
	}))
}

// prefilterHeader is the section header of an on or conditional prefilter stage.
func prefilterHeader(st query.StagePlan) string {
	if st.State == query.StageConditional {
		return fmt.Sprintf("prefilter (%s: %s)", st.Note, st.Scope)
	}
	return fmt.Sprintf("prefilter (%s)", st.Scope)
}

// conjunctsWhere returns the conjuncts for which keep is true, in order.
func conjunctsWhere(cs []conjunctPlan, keep func(conjunctPlan) bool) []conjunctPlan {
	var rows []conjunctPlan
	for _, c := range cs {
		if keep(c) {
			rows = append(rows, c)
		}
	}
	return rows
}

// writePrefilterRows writes one tree per conjunct. Two or more rows nest under an
// `and` line.
func writePrefilterRows(b *strings.Builder, sp sourcePlan, rows []conjunctPlan) {
	indent := 2
	if len(rows) > 1 {
		b.WriteString("  and\n")
		indent = 4
	}
	for _, c := range rows {
		writeNodeRows(b, indent, c.node, prefilterNote(sp, c))
	}
}

// prefilterNote is the note of a prefilter row: the jq source of the conjunct, and
// the reason the server did not take it.
func prefilterNote(sp sourcePlan, c conjunctPlan) string {
	note := "— " + c.Expr
	if sp.server.State != query.StageNone && c.Server == nil {
		note += "  (not pushed: " + declinedReason + ")"
	}
	return note
}

// writeNodeRows writes a compiled node. An and or or node is a tree: the operator on
// the first line with the note, and its children two spaces deeper. Any other node
// is one line in its neutral notation.
func writeNodeRows(b *strings.Builder, indent int, n predicate.Node, note string) {
	var op string
	var kids []predicate.Node
	switch v := n.(type) {
	case predicate.And:
		op, kids = "and", v
	case predicate.Or:
		op, kids = "or", v
	default:
		writeNoteRow(b, indent, n.String(), note)
		return
	}
	writeNoteRow(b, indent, op, note)
	for _, k := range kids {
		writeNodeRows(b, indent+2, k, "")
	}
}

// writeJQSection appends the jq section. Its rows are the conjuncts that no earlier
// stage evaluates.
func writeJQSection(b *strings.Builder, sp sourcePlan) {
	const header = "jq (after decode, the full filter, every conjunct again)"
	rows := conjunctsWhere(sp.conjuncts, conjunctPlan.jqOnly)
	if len(rows) == 0 {
		b.WriteString("\n" + planHeader(header) + "\n")
		return
	}
	writePlanSection(b, header)
	for _, c := range rows {
		writeNoteRow(b, 2, "only here: "+c.Expr, "— "+jqRowReason(sp, c))
	}
}

// jqRowReason is the note of a jq row. A conjunct that did not compile carries its
// own reason.
func jqRowReason(sp sourcePlan, c conjunctPlan) string {
	if c.Reason != "" {
		return c.Reason
	}
	return "not pushed: " + jqOnlyReason(sp, c)
}

// jqOnlyReason is why a compiled conjunct reaches only jq. The server declined it,
// or the store has no server stage and the note of that stage says why.
func jqOnlyReason(sp sourcePlan, c conjunctPlan) string {
	if r, ok := c.Skipped[stageServer]; ok {
		return r
	}
	return sp.server.Note
}

// writeNoteRow writes one row: the indent, the left text padded to noteColumn, and
// the note in the faint color. The padding comes before the color, so ANSI escapes
// never skew the column. A left text that fills the column gets two spaces. An empty
// note writes the left text alone. The left text may carry its own color.
func writeNoteRow(b *strings.Builder, indent int, left, note string) {
	b.WriteString(strings.Repeat(" ", indent) + left)
	if note == "" {
		b.WriteByte('\n')
		return
	}
	pad := max(noteColumn-(indent-sectionIndent)-jqfmt.VisibleWidth(left), 2)
	b.WriteString(strings.Repeat(" ", pad) + pal.faint.Sprint(note) + "\n")
}

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
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

// conjunctStages plans the stages of one conjunct on a streaming route. A conjunct
// that did not compile is for jq only. A compiled conjunct goes to the server if the
// translator of the driver gives it a fragment, to the prefilter if that stage runs,
// and always to jq. The stage states of the whole predicate (ap) decide which stages
// the plan names. The plan never names a stage whose state is none.
func conjunctStages(d driver, url string, keys selector.KeySet, ap query.AccessPlan, cj pushdown.Conjunct, unbounded bool) (conjunctPlan, error) {
	if cj.Pred == nil {
		return conjunctPlan{Expr: cj.Expr, Stages: []string{stageJQ}, Reason: "not compiled: " + cj.Reason}, nil
	}
	c := conjunctPlan{Expr: cj.Expr, Compiled: cj.Pred.String(), node: cj.Pred}
	if ap.Server.State != query.StageNone {
		cp, err := d.explainPlan(url, keys, cj.Pred, unbounded)
		if err != nil {
			return conjunctPlan{}, fmt.Errorf("plan %s conjunct: %w", d.name, err)
		}
		c.Server = cp.Filter
	}
	if c.Server != nil {
		c.Stages = append(c.Stages, stageServer)
	} else if ap.Server.State != query.StageNone {
		c.skip(stageServer, declinedReason)
	}
	switch ap.Prefilter.State {
	case query.StageOn, query.StageConditional:
		c.Stages = append(c.Stages, stagePrefilter)
	case query.StageOff:
		c.skip(stagePrefilter, ap.Prefilter.Note)
	}
	c.Stages = append(c.Stages, stageJQ)
	return c, nil
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
// way. A scalar never breaks.
func fitJSON(v any, indent, width int) ([]string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode JSON: %w", err)
	}
	dec := json.NewDecoder(&buf)
	dec.UseNumber()
	var norm any
	if err := dec.Decode(&norm); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	return layoutJSON(norm, indent, "", "", width)
}

// layoutJSON lays out a normalized value (maps, slices, json.Number, string, bool,
// nil). The prefix is the key text before the value, and the suffix is the comma
// after it.
func layoutJSON(v any, indent int, prefix, suffix string, width int) ([]string, error) {
	pad := strings.Repeat(" ", indent)
	compact, err := compactJSON(v)
	if err != nil {
		return nil, err
	}
	if indent+utf8.RuneCountInString(prefix+compact+suffix) <= width {
		return []string{pad + prefix + compact + suffix}, nil
	}
	var open, closer string
	var members []string
	var values []any
	switch t := v.(type) {
	case map[string]any:
		open, closer = "{", "}"
		for _, k := range slices.Sorted(maps.Keys(t)) {
			key, err := jsonString(k)
			if err != nil {
				return nil, err
			}
			members = append(members, key+": ")
			values = append(values, t[k])
		}
	case []any:
		open, closer = "[", "]"
		for _, e := range t {
			members = append(members, "")
			values = append(values, e)
		}
	default:
		return []string{pad + prefix + compact + suffix}, nil
	}
	lines := []string{pad + prefix + open}
	for i, m := range values {
		sep := ","
		if i == len(values)-1 {
			sep = ""
		}
		member, err := layoutJSON(m, indent+2, members[i], sep, maxJSONWidth)
		if err != nil {
			return nil, err
		}
		lines = append(lines, member...)
	}
	return append(lines, pad+closer+suffix), nil
}

// compactJSON is the one-line form of a normalized value.
func compactJSON(v any) (string, error) {
	switch t := v.(type) {
	case map[string]any:
		parts := make([]string, 0, len(t))
		for _, k := range slices.Sorted(maps.Keys(t)) {
			key, err := jsonString(k)
			if err != nil {
				return "", err
			}
			val, err := compactJSON(t[k])
			if err != nil {
				return "", err
			}
			parts = append(parts, key+": "+val)
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			val, err := compactJSON(e)
			if err != nil {
				return "", err
			}
			parts = append(parts, val)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case string:
		return jsonString(t)
	case json.Number:
		return t.String(), nil
	case bool:
		if t {
			return "true", nil
		}
		return "false", nil
	default:
		return "null", nil
	}
}

// jsonString is a JSON string literal with no HTML escaping.
func jsonString(s string) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return "", fmt.Errorf("encode JSON string: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// writePrefilterSection appends the prefilter section. Each row is a compiled
// conjunct. Two or more conjuncts nest under an `and` line.
func writePrefilterSection(b *strings.Builder, sp sourcePlan) {
	st := sp.prefilter
	header := fmt.Sprintf("prefilter (%s)", st.Scope)
	if st.State == query.StageConditional {
		header = fmt.Sprintf("prefilter (%s: %s)", st.Note, st.Scope)
	}
	if st.State == query.StageNone {
		return
	}
	if !writeStageHeader(b, "prefilter", st, header) {
		return
	}
	var rows []conjunctPlan
	for _, c := range sp.conjuncts {
		if c.has(stagePrefilter) {
			rows = append(rows, c)
		}
	}
	indent := 2
	if len(rows) > 1 {
		b.WriteString("  and\n")
		indent = 4
	}
	for _, c := range rows {
		note := "— " + c.Expr
		if sp.server.State != query.StageNone && c.Server == nil {
			note += "  (not pushed: " + declinedReason + ")"
		}
		writeNodeRows(b, indent, c.node, note)
	}
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
	var rows []conjunctPlan
	for _, c := range sp.conjuncts {
		if c.jqOnly() {
			rows = append(rows, c)
		}
	}
	if len(rows) == 0 {
		b.WriteString("\n" + planHeader(header) + "\n")
		return
	}
	writePlanSection(b, header)
	for _, c := range rows {
		reason := c.Reason
		if reason == "" {
			reason = "not pushed: " + jqOnlyReason(sp, c)
		}
		writeNoteRow(b, 2, "only here: "+c.Expr, "— "+reason)
	}
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

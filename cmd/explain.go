package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fatih/color"
	"github.com/itchyny/gojq"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/jqfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/pushdown"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/render"
	"github.com/zsltg/iq/internal/selector"
)

// sourcePlan is the computed access plan for one source: the driver's stable
// name, the query's classification, the planned backend operations, the
// server-side filter (nil when the backend filters nothing), and the per-conjunct
// pushdown decisions. Both the pretty --explain/--verbose text and the structured
// "query plan" log record read from this one assembly (buildSourcePlan), so the
// two never drift. hasPlan is false when the scheme has no registered describer,
// so the caller contributes no backend section and logs no plan.
type sourcePlan struct {
	hasPlan   bool
	driver    string
	keys      selector.KeySet
	ops       []string
	filter    map[string]any
	conjuncts []conjunctPlan
}

// conjunctPlan is one top-level conjunct's pushdown outcome: the jq source, the
// pushed decision, and the reason it stayed client-side (empty when pushed). Its
// fields are exported so slog.Any renders it as a JSON object under the json
// format — the CI-parseable per-conjunct contract.
type conjunctPlan struct {
	Expr   string `json:"expr"`
	Pushed bool   `json:"pushed"`
	Reason string `json:"reason,omitempty"`
}

// buildSourcePlan computes the access plan for one source without connecting: it
// classifies filter, compiles the pushable predicate (only when compile is set,
// matching how execution gates the push), and asks the driver's describer what
// backend calls it would make. An unrecognized scheme or a driver with no
// describer yields hasPlan false. A filter parse failure is returned as a syntax
// error. It is the single source both the text renderer and the log record use.
func buildSourcePlan(url, filter string, compile, unbounded bool) (sourcePlan, error) {
	q, err := gojq.Parse(filter)
	if err != nil {
		return sourcePlan{}, asSyntaxError(filter, err)
	}
	return buildSourcePlanQuery(url, q, compile, unbounded), nil
}

// buildSourcePlanQuery is buildSourcePlan for an already-parsed query, so the
// --explain text path (which parses the filter once up front) does not re-parse it.
func buildSourcePlanQuery(url string, q *gojq.Query, compile, unbounded bool) sourcePlan {
	d, ok := driverForScheme(schemeOf(url))
	if !ok || d.explainPlan == nil {
		return sourcePlan{}
	}
	keys := selector.Keys(q)
	var pred predicate.Node
	var conjuncts []pushdown.Conjunct
	if compile {
		if p, ok := pushdown.Compile(q); ok {
			pred = p
		}
		conjuncts, _ = pushdown.Conjuncts(q)
	}
	ap := d.explainPlan(keys, pred, unbounded)
	sp := sourcePlan{hasPlan: true, driver: d.name, keys: keys, ops: ap.Ops, filter: ap.Filter}
	for _, cj := range conjuncts {
		pushed, reason := conjunctDecision(d, keys, cj, unbounded)
		sp.conjuncts = append(sp.conjuncts, conjunctPlan{Expr: cj.Expr, Pushed: pushed, Reason: reason})
	}
	return sp
}

// logAttrs renders the source plan as the structured attributes of the "query
// plan" log record: the source handle, the driver, a classification group
// {keys, streamable, scan}, the planned ops, the server-side filter (as a nested
// object, omitted when none), and the per-conjunct decisions. Keys are attrs
// only, never interpolated into the message — the documented stable-key contract.
func (sp sourcePlan) logAttrs(handle string) []any {
	attrs := []any{
		slog.String("handle", handle),
		slog.String("driver", sp.driver),
		slog.Group(
			"classification",
			slog.Any("keys", sp.keys.Keys),
			slog.Bool("streamable", sp.keys.Streamable),
			slog.Bool("scan", sp.keys.Scan),
		),
		slog.Any("ops", sp.ops),
	}
	if sp.filter != nil {
		attrs = append(attrs, slog.Any("filter", sp.filter))
	}
	if len(sp.conjuncts) > 0 {
		attrs = append(attrs, slog.Any("conjuncts", sp.conjuncts))
	}
	return attrs
}

// buildJQPlan renders the query plan for the default jq action: the pretty-printed
// filter (with any nested source() sub-filters inlined) and, for a single-source
// filter, the backend calls its store will make. The source must already be
// resolved for the non-cross case, so the plan can name the driver without
// connecting. It is shown by --explain (to stdout, no execution) and --verbose (to
// stderr, before executing).
func buildJQPlan(cfg *config, filter string, cross, describe bool) (string, error) {
	q, err := gojq.Parse(filter)
	if err != nil {
		return "", asSyntaxError(filter, err)
	}
	var b strings.Builder
	writePlanTitle(&b)
	if cross {
		writePlanLine(&b, "target", "cross-source (reads through source())")
	} else {
		writePlanLine(&b, "source", fmt.Sprintf("%s (%s)", cfg.handle, driverName(cfg.url)))
	}
	writePlanSection(&b, "jq filter")
	writeJQFilterQuery(&b, q, describe, cfg.unbounded, !cross)
	if !cross {
		if err := writeAccessPlan(&b, cfg.url, q, !cfg.noCompile, cfg.unbounded); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

// buildCombinePlan renders the query plan for `iq combine`: each stage's source,
// driver, pretty-printed reducer, and backend calls, then the final --with program.
func buildCombinePlan(cfg *config, stages []combineStage, with string, describe bool) (string, error) {
	var b strings.Builder
	writePlanTitle(&b)
	writePlanLine(&b, "mode", "cross-source combine")
	for _, st := range stages {
		q, err := gojq.Parse(st.spec.filter)
		if err != nil {
			return "", asSyntaxError(st.spec.filter, err)
		}
		writePlanSection(&b, fmt.Sprintf("$%s  <-  %s (%s)", st.varName, st.spec.handle, driverName(st.spec.url)))
		writeJQFilterQuery(&b, q, describe, cfg.unbounded, true)
		if err := writeAccessPlan(&b, st.spec.url, q, !cfg.noCompile, cfg.unbounded); err != nil {
			return "", err
		}
	}
	writePlanSection(&b, "combine (over the bound $vars, null input)")
	cq, err := gojq.Parse(with)
	if err != nil {
		return "", asSyntaxError(with, err)
	}
	// The combine program runs over the bound $vars with null input — no source, so
	// no data-access mark (markable is false).
	writeJQFilterQuery(&b, cq, describe, cfg.unbounded, false)
	if cfg.insert != "" {
		if err := writeCombineWritePlan(&b, cfg); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

// writeCombineWritePlan appends the destination and its write operations, so an
// --explain of a writing combine does not read as though it only reads.
func writeCombineWritePlan(b *strings.Builder, cfg *config) error {
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	dst, err := resolveEndpoint(cf, cfg.insert, true)
	if err != nil {
		return err
	}
	// The plan must refuse what the run refuses. runInsert rejects stdio, and a
	// backend with no write describer is one that cannot be written to at all (the
	// file driver is read-only) — rendering a bare write section for either would
	// describe a run that cannot happen.
	if dst.isFile {
		return errors.New("--insert must name a saved source, not stdout")
	}
	d, ok := driverForScheme(schemeOf(dst.url))
	if !ok || d.explainWrite == nil {
		return fmt.Errorf("destination %s (%s) cannot be written to", dst.label(), dst.driver)
	}
	mode := query.Upsert
	if cfg.noOverwrite {
		mode = query.InsertOnly
	}
	writePlanSection(b, "write  ->  "+dst.label())
	for _, op := range d.explainWrite(mode).Ops {
		b.WriteString("  " + op + "\n")
	}
	return nil
}

// writeJQFilterQuery renders a parsed filter under the "jq filter" section: annotated
// per pipe stage when describe is set, otherwise the compact pretty print. When
// markable (a resolved single source), the root data-access stage is tagged with its
// route; a cross-source or source-less program (the --combine step) passes markable
// false. The query is already parsed, so this path re-uses it and never re-parses.
func writeJQFilterQuery(b *strings.Builder, q *gojq.Query, describe, unbounded, markable bool) {
	pretty := jqfmt.FormatQuery(q, colorOn())
	if !describe {
		writeIndented(b, pretty, 2)
		return
	}
	var mark *accessMark
	if markable {
		mark = accessMarkForQuery(q, unbounded)
	}
	writeJQExplained(b, pretty, jqfmt.ExplainQuery(q, colorOn()), 2, mark)
}

// accessMark labels the pipe stage that reads from the store: which stage (by index
// in the Explain stage list) performs the root data access, the human name of the
// route, and the color that names its cost (green bounded read, yellow streaming
// scan, red materialized scan).
type accessMark struct {
	stage int
	label string
	c     *color.Color
}

// accessMarkForQuery classifies a single-source query's root data access into an
// accessMark. The route comes from the selector's key/scan classification — the same
// driver-agnostic verdict the access plan uses — with --unbounded downgrading a
// streamable scan to a materialized one, since it buffers the whole dataset. The
// responsible stage is the first body stage: index 1 when leading declarations
// (module/imports/func-defs) occupy stage 0, otherwise stage 0.
func accessMarkForQuery(q *gojq.Query, unbounded bool) *accessMark {
	label, c := classifyAccess(selector.Keys(q), unbounded)
	stage := 0
	if jqfmt.HasLeadingDecls(q) {
		stage = 1
	}
	return &accessMark{stage: stage, label: label, c: c}
}

// classifyAccess names the route the KeySet describes and its cost color: bounded
// keys are a cheap keyed read (green); a scan that distributes over `.[]` streams
// batch by batch (yellow), unless --unbounded forces it to materialize; any other
// scan collapses the whole keyspace into memory (red).
func classifyAccess(keys selector.KeySet, unbounded bool) (string, *color.Color) {
	switch {
	case !keys.Scan:
		return "bounded read", pal.ok
	case keys.Streamable && !unbounded:
		return "streaming scan", pal.change
	default:
		return "materialized scan", pal.fail
	}
}

// writeJQExplained renders the pretty-printed filter with a right-aligned em-dash
// note on each pipe stage's first line, matching writeConjunctLine's style. The body
// is the compact render (jqfmt.Format) verbatim — pipe connectors and all — so the
// annotated form differs from the plain form only by the note column. A stage spans
// the same number of lines inline as standalone, so each stage's note attaches to the
// physical line where that stage begins, found by accumulating stage line counts. The
// stage named by mark (the root data access) is colored by its route cost and leads
// with the route name; every other note is dimmed. The note column is the widest
// annotated line by visible width, so ANSI escapes never skew it; a multi-line stage
// carries its note on the first line only.
func writeJQExplained(b *strings.Builder, pretty string, stages []jqfmt.Stage, indent int, mark *accessMark) {
	lines := strings.Split(pretty, "\n")
	notes := make(map[int]string, len(stages))
	at := 0
	for i, s := range stages {
		notes[at] = stageNote(s.Desc, i, mark)
		at += strings.Count(s.Text, "\n") + 1
	}
	col := 0
	for i := range lines {
		if notes[i] != "" {
			if w := jqfmt.VisibleWidth(lines[i]); w > col {
				col = w
			}
		}
	}
	pad := strings.Repeat(" ", indent)
	for i, line := range lines {
		if line == "" {
			b.WriteByte('\n')
			continue
		}
		b.WriteString(pad + line)
		if note := notes[i]; note != "" {
			b.WriteString(strings.Repeat(" ", col-jqfmt.VisibleWidth(line)) + "  " + note)
		}
		b.WriteByte('\n')
	}
}

// stageNote builds one stage's note. Every stage shows its dimmed jq description; the
// root data-access stage (mark.stage) appends its route name in parentheses, colored
// by cost. When that stage has no description, the colored route stands alone as the
// note. Returns "" when there is nothing to show.
func stageNote(desc string, stage int, mark *accessMark) string {
	marked := mark != nil && stage == mark.stage
	switch {
	case marked && desc != "":
		return pal.faint.Sprint("— "+desc) + " " + mark.c.Sprint("("+mark.label+")")
	case marked:
		return mark.c.Sprint("— " + mark.label)
	case desc != "":
		return pal.faint.Sprint("— " + desc)
	default:
		return ""
	}
}

// writeAccessPlan appends the backend-calls section for one source: the driver's
// planned operations, a per-conjunct pushdown breakdown, and, when a predicate
// pushes to the store, the compiled server-side filter as colored JSON. A driver
// with no describer, or an unrecognized scheme, contributes nothing. The predicate
// is compiled here only when pushdown is enabled (the default; disabled by
// --no-compile), matching how execution gates the push.
func writeAccessPlan(b *strings.Builder, url string, q *gojq.Query, compile, unbounded bool) error {
	sp := buildSourcePlanQuery(url, q, compile, unbounded)
	if !sp.hasPlan {
		return nil
	}
	writePlanSection(b, sp.driver+" calls")
	for _, op := range sp.ops {
		b.WriteString("  " + op + "\n")
	}
	writePushdownDecisions(b, sp.conjuncts)
	if sp.filter != nil {
		js, err := render.JSON(sp.filter, colorOn())
		if err != nil {
			return fmt.Errorf("render pushed filter: %w", err)
		}
		b.WriteString("  filter:\n")
		writeIndented(b, js, 4)
	}
	return nil
}

// writePushdownDecisions appends the per-conjunct pushdown breakdown: for each
// top-level conjunct of the filter's select(...) predicate, whether the backend
// evaluates it server-side (pushed) or it falls to the client-side jq re-run
// (client-side), and why a client-side conjunct did not push. It mirrors Trino's
// EXPLAIN pushdown report — the observable split of what the backend evaluated
// versus what ran client-side. The pushed conjuncts' merged fragment is the filter
// block that follows, rendered by the same scan translator the query itself uses;
// this section adds the decisions and reasons, never a second rendering. Nothing is
// written when the filter has no analysable conjuncts (a non-streamable filter, no
// select, or pushdown disabled).
func writePushdownDecisions(b *strings.Builder, conjuncts []conjunctPlan) {
	if len(conjuncts) == 0 {
		return
	}
	writePlanSection(b, "pushdown")
	for _, cj := range conjuncts {
		writeConjunctLine(b, cj.Pushed, cj.Expr, cj.Reason)
	}
}

// conjunctDecision resolves one conjunct to pushed (true, no reason) or client-side
// (false, with the reason). A conjunct the compiler could not turn into a predicate
// carries its own compile-stage reason. A compiled conjunct is then offered to the
// driver's scan translator through the same describer the plan uses: a backend that
// narrows on it (a non-nil pushed fragment) pushes it; one whose translator declines
// it, or a driver with no scan-side filter at all, re-runs it client-side.
func conjunctDecision(d driver, keys selector.KeySet, cj pushdown.Conjunct, unbounded bool) (bool, string) {
	if cj.Pred == nil {
		return false, cj.Reason
	}
	if !d.filtersScan {
		return false, "store applies no server-side filter"
	}
	if d.explainPlan(keys, cj.Pred, unbounded).Filter == nil {
		return false, "declined by backend translator"
	}
	return true, ""
}

// writeConjunctLine writes one decision row: a colored pushed/client-side label, the
// conjunct's jq source, and, for a client-side conjunct, the reason it did not push.
// The label word is padded before it is colored so the ANSI escapes never skew the
// column alignment (the color helpers render plain when color is off).
func writeConjunctLine(b *strings.Builder, pushed bool, expr, reason string) {
	const w = len("client-side")
	label, c := "pushed", pal.ok
	if !pushed {
		label, c = "client-side", pal.change
	}
	b.WriteString("  " + c.Sprint(label) + strings.Repeat(" ", w-len(label)) + "  " + expr)
	if reason != "" {
		b.WriteString("  " + pal.faint.Sprint("— "+reason))
	}
	b.WriteByte('\n')
}

// writePlanTitle writes the plan's title line.
func writePlanTitle(b *strings.Builder) {
	b.WriteString(planHeader("query plan") + "\n")
}

// writePlanLine writes a `key: value` line with a colored key.
func writePlanLine(b *strings.Builder, key, value string) {
	b.WriteString(planHeader(key+":") + " " + value + "\n")
}

// writePlanSection writes a blank line then a colored section label.
func writePlanSection(b *strings.Builder, label string) {
	b.WriteString("\n" + planHeader(label+":") + "\n")
}

// planHeader colors a label with the shared header style when color is on.
func planHeader(s string) string {
	if colorOn() {
		return pal.header.Sprint(s)
	}
	return s
}

// writeIndented appends text with every non-empty line prefixed by n spaces, so a
// multi-line block nests under its label.
func writeIndented(b *strings.Builder, text string, n int) {
	pad := strings.Repeat(" ", n)
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			b.WriteByte('\n')
			continue
		}
		b.WriteString(pad + line + "\n")
	}
}

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
	"github.com/zsltg/iq/internal/selector"
)

// sourcePlan is the computed access plan for one source: the driver name, the
// query classification, the planned backend operations, the merged server filter
// (nil when none is planned), the server and prefilter stages of the whole
// predicate, and the per-conjunct stage plans. The pretty --explain/--verbose text
// and the structured "query plan" log record both read from this one assembly
// (buildSourcePlan), so they never drift. hasPlan is false when the scheme has no
// registered describer. Then the caller adds no backend section and logs no plan.
// On the materialized route the stage plans stay empty, because the scan runs
// neither stage.
type sourcePlan struct {
	hasPlan      bool
	driver       string
	keys         selector.KeySet
	ops          []string
	filter       map[string]any
	server       query.StagePlan
	prefilter    query.StagePlan
	materialized bool
	conjuncts    []conjunctPlan
}

// conjunctPlan is the stage plan of one top-level conjunct: its jq source, the
// one-line form of its compiled node, its server fragment, the ordered stages that
// evaluate it, the stages that skip it with the reason, and the reason that only
// jq sees it. Its fields are exported so slog.Any renders it as a JSON object under
// the json format. That is the CI-parseable per-conjunct contract.
type conjunctPlan struct {
	Expr     string            `json:"expr"`
	Compiled string            `json:"compiled,omitempty"`
	Server   map[string]any    `json:"server,omitempty"`
	Stages   []string          `json:"stages"`
	Skipped  map[string]string `json:"skipped,omitempty"`
	Reason   string            `json:"reason,omitempty"`

	// node is the compiled predicate. The text prefilter section renders it as a tree.
	node predicate.Node
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
	return buildSourcePlanQuery(url, q, compile, unbounded)
}

// materializedReason is why a conjunct stays jq-only on the materialized route.
const materializedReason = "materialized: the scan reads every record"

// Stage names in conjunctPlan.Stages and conjunctPlan.Skipped.
const (
	stageServer    = "server"
	stagePrefilter = "prefilter"
	stageJQ        = "jq"
)

// declinedReason is why a compiled conjunct has no server fragment.
const declinedReason = "declined by backend translator"

// buildSourcePlanQuery is buildSourcePlan for an already-parsed query, so the
// --explain text path (which parses the filter once up front) does not re-parse it.
// It follows JQEngine.Run: with unbounded set, a scan is materialized and pushes
// nothing. Then the driver gets no predicate, and every conjunct reaches only jq,
// with materializedReason. The unbounded flag alone decides this. Only a
// streamable scan has conjuncts, and a bounded read ignores the predicate.
func buildSourcePlanQuery(url string, q *gojq.Query, compile, unbounded bool) (sourcePlan, error) {
	d, ok := driverForScheme(schemeOf(url))
	if !ok || d.explainPlan == nil {
		return sourcePlan{}, nil
	}
	keys := selector.Keys(q)
	materialized := unbounded
	pred, conjuncts := compiledPredicate(q, compile, materialized)
	ap, err := d.explainPlan(url, keys, pred, unbounded)
	if err != nil {
		return sourcePlan{}, fmt.Errorf("plan %s source: %w", d.name, err)
	}
	plans, err := conjunctPlans(d, url, keys, ap, conjuncts, unbounded, materialized)
	if err != nil {
		return sourcePlan{}, err
	}
	sp := sourcePlan{
		hasPlan:      true,
		driver:       d.name,
		keys:         keys,
		ops:          ap.Ops,
		filter:       ap.Filter,
		materialized: materialized,
		conjuncts:    plans,
	}
	if !materialized {
		sp.server, sp.prefilter = ap.Server, ap.Prefilter
	}
	return sp, nil
}

// compiledPredicate returns the predicate to give the driver and the conjuncts
// to report. With compile off, it returns neither. On the materialized route, it
// returns the conjuncts but no predicate, because execution pushes nothing.
func compiledPredicate(q *gojq.Query, compile, materialized bool) (predicate.Node, []pushdown.Conjunct) {
	if !compile {
		return nil, nil
	}
	conjuncts, _ := pushdown.Conjuncts(q)
	if materialized {
		return nil, conjuncts
	}
	if p, ok := pushdown.Compile(q); ok {
		return p, conjuncts
	}
	return nil, conjuncts
}

// conjunctPlans resolves each conjunct to its stage plan. On the materialized
// route, every conjunct reaches only jq, with materializedReason.
func conjunctPlans(d driver, url string, keys selector.KeySet, ap query.AccessPlan, conjuncts []pushdown.Conjunct, unbounded, materialized bool) ([]conjunctPlan, error) {
	var plans []conjunctPlan
	cx := conjunctContext{driver: d, url: url, keys: keys, plan: ap, unbounded: unbounded}
	for _, cj := range conjuncts {
		if materialized {
			plans = append(plans, conjunctPlan{Expr: cj.Expr, Stages: []string{stageJQ}, Reason: materializedReason})
			continue
		}
		c, err := conjunctStages(cx, cj)
		if err != nil {
			return nil, err
		}
		plans = append(plans, c)
	}
	return plans, nil
}

// logAttrs renders the source plan as the structured attributes of the "query
// plan" log record: the source handle, the driver, a classification group
// {keys, streamable, scan}, the planned ops, the server-side filter (a nested
// object, omitted when none), the server and prefilter stage plans of a scan
// (omitted on the materialized route), and the per-conjunct stage plans. Keys are attrs
// only, never interpolated into the message. This is the documented stable-key
// contract.
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
	if sp.keys.Scan && !sp.materialized {
		attrs = append(attrs, slog.Any("server", sp.server), slog.Any("prefilter", sp.prefilter))
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
	// The plan must refuse what the run refuses. runInsert rejects stdio. A
	// backend with no write describer cannot be written to at all (the file
	// driver is read-only). A write section for either describes a run
	// that cannot happen.
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
// route; a cross-source or source-less program (the combine --with step) passes markable
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

// accessMark labels the pipe stage that reads from the store: the index of that
// stage in the Explain stage list, the human name of the route, and the color
// of its cost (green bounded read, yellow streaming scan, red materialized
// scan).
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

// classifyAccess names the route that the KeySet describes and its cost color.
// Bounded keys are a cheap keyed read (green). A scan that distributes over
// `.[]` streams batch by batch (yellow), unless --unbounded forces it to
// materialize. Any other scan collapses the whole keyspace into memory (red).
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

// writeJQExplained renders the pretty-printed filter with a right-aligned
// em-dash note on the first line of each pipe stage, in the style of
// writeConjunctLine. The body is the compact render (jqfmt.Format) verbatim, so
// the annotated form differs from the plain form only by the note column. A
// stage spans the same number of lines inline as standalone, so each note
// attaches to the physical line where its stage begins, found by accumulating
// stage line counts. The stage named by mark (the root data access) is colored
// by its route cost and leads with the route name. Every other note is dimmed.
// The note column is the widest annotated line by visible width, so ANSI
// escapes never skew it.
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

// writeAccessPlan appends the plan of one source: the backend calls, then one
// section per filter stage (server, prefilter, jq). A driver with no describer, or
// an unrecognized scheme, contributes nothing. The predicate is compiled only when
// pushdown is enabled (the default, disabled by --no-compile), matching how
// execution gates the push.
func writeAccessPlan(b *strings.Builder, url string, q *gojq.Query, compile, unbounded bool) error {
	sp, err := buildSourcePlanQuery(url, q, compile, unbounded)
	if err != nil {
		return err
	}
	if !sp.hasPlan {
		return nil
	}
	writePlanSection(b, sp.driver+" calls")
	for _, op := range sp.ops {
		b.WriteString("  " + op + "\n")
	}
	return writeStageSections(b, sp)
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
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			b.WriteByte('\n')
			continue
		}
		b.WriteString(pad + line + "\n")
	}
}

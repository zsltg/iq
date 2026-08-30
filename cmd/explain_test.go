package cmd

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/selector"
)

func TestBuildJQPlanSingleSourceMongo(t *testing.T) {
	// A resolved single-source plan pretty-prints the filter and, with pushdown on
	// by default, shows the pushed MongoDB filter as the backend call.
	cfg := &config{url: "mongodb://localhost:27017/shop", handle: "orders"}
	out, err := buildJQPlan(cfg, ".[] | select(.total > 99) | {id, total}", false, false)
	require.NoError(t, err)
	require.Contains(t, out, "source: orders (mongo)")
	// jq is broken onto pipe stages.
	require.Contains(t, out, "\n  .[]\n")
	require.Contains(t, out, "\n  | select(.total > 99)\n")
	// The backend section names the driver and shows the pushed filter.
	require.Contains(t, out, "mongo calls:")
	require.Contains(t, out, "find(<filter>)")
	require.Contains(t, out, `"$gt": 99`)
}

func TestBuildJQPlanDescribedAnnotatesStages(t *testing.T) {
	// With describe set (--explain --verbose), the jq-filter block annotates each pipe
	// stage with a right-aligned dimmed em-dash note; the dashes align on the widest
	// stage first line. Color is off under `go test`, so the exact bytes are asserted.
	cfg := &config{url: "mongodb://localhost:27017/shop", handle: "orders"}
	filter := ".users[] | select(.age > 30) | {name, city: .addr.city}"
	out, err := buildJQPlan(cfg, filter, false, true)
	require.NoError(t, err)

	// The root .users[] stage shows its dimmed jq description followed by its route in
	// parentheses (leading .users key → bounded read); later stages stay plain notes.
	want := "  .users[]" + strings.Repeat(" ", 13) + "— each element of .users (bounded read)\n" +
		"  | select(.age > 30)  — keep inputs where .age > 30\n" +
		"  | {" + strings.Repeat(" ", 18) + "— build an object (name, city)\n" +
		"    name,\n    city: .addr.city\n  }\n"
	require.Contains(t, out, want)
}

func TestBuildJQPlanDescribeFalseStaysCompact(t *testing.T) {
	// Plain --explain (describe false) is byte-for-byte the compact path: no em-dash
	// notes, and the pipe-joined stage layout Format produces.
	cfg := &config{url: "mongodb://localhost:27017/shop", handle: "orders"}
	filter := ".users[] | select(.age > 30) | {name, city: .addr.city}"
	out, err := buildJQPlan(cfg, filter, false, false)
	require.NoError(t, err)
	require.NotContains(t, out, "—")
	require.Contains(t, out, "\n  .users[]\n  | select(.age > 30)\n")
}

func TestBuildJQPlanMongoNoCompileNoFilter(t *testing.T) {
	// With --no-compile the plan shows a full-collection scan and no pushed filter,
	// and pushdown is off entirely, so there is no per-conjunct breakdown either.
	cfg := &config{url: "mongodb://localhost:27017/shop", handle: "orders", noCompile: true}
	out, err := buildJQPlan(cfg, ".[] | select(.total > 99)", false, false)
	require.NoError(t, err)
	require.Contains(t, out, "full-collection scan")
	require.NotContains(t, out, "  filter:")
	require.NotContains(t, out, "pushdown:")
}

func TestBuildJQPlanRedisScan(t *testing.T) {
	cfg := &config{url: "redis://localhost:6379", handle: "cache"}
	out, err := buildJQPlan(cfg, ".[] | select(.active)", false, false)
	require.NoError(t, err)
	require.Contains(t, out, "source: cache (redis)")
	require.Contains(t, out, "redis calls:")
	require.Contains(t, out, "SCAN 0 MATCH * COUNT 100")
	require.Contains(t, out, "no server-side filter")
	require.NotContains(t, out, "  filter:")
	// A bare truthiness test is not a pushable comparison, so the one conjunct is
	// reported client-side with that reason.
	require.Contains(t, out, "pushdown:")
	require.Contains(t, out, "client-side")
	require.Contains(t, out, "not a pushable comparison")
}

func TestBuildJQPlanCrossSourceInlinesNested(t *testing.T) {
	// A cross-source filter is labeled as such and inlines the nested source()
	// sub-filter, pretty-printed, without resolving a primary source.
	cfg := &config{}
	out, err := buildJQPlan(cfg, `source("orders"; ".[] | select(.vip)") | length`, true, false)
	require.NoError(t, err)
	require.Contains(t, out, "cross-source")
	require.Contains(t, out, `source("orders";`)
	require.Contains(t, out, "select(.vip)")
	require.NotContains(t, out, "calls:") // no backend section for cross-source
}

func TestBuildJQPlanReportsSyntaxError(t *testing.T) {
	cfg := &config{url: "redis://localhost:6379", handle: "cache"}
	_, err := buildJQPlan(cfg, ".[ | broken", false, false)
	require.Error(t, err)
}

func TestWriteAccessPlanUnknownSchemeIsSilent(t *testing.T) {
	// An unrecognized scheme (no registered driver) contributes no backend section.
	var b strings.Builder
	require.NoError(t, writeAccessPlan(&b, "postgres://x/y", mustParseCmd(t, ".[]"), false, false))
	require.Empty(t, b.String())
}

func TestExplainFlagPrintsPlanAndStopsBeforeConnecting(t *testing.T) {
	// --explain drives the plan through the real command and returns before any
	// connection, so it works offline against an unreachable host. A short timeout
	// guarantees that any path which instead falls through to execute fails fast
	// rather than hanging.
	t.Run("default jq action", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("orders", "mongodb://h/shop"))
		seedConfig(t, c)

		root, _ := newRootCmd()
		out, err := runCmd(t, root, "--src", "orders", "--timeout", "200ms", "--explain",
			".[] | select(.total > 99) | {id, total}")
		require.NoError(t, err)
		require.Contains(t, out, "query plan")
		require.Contains(t, out, "source: orders (mongo)")
		require.Contains(t, out, "mongo calls:")
		require.Contains(t, out, `"$gt": 99`)
	})

	t.Run("cross-source combine action", func(t *testing.T) {
		c := newSeed()
		require.NoError(t, c.Add("orders", "mongodb://h/shop"))
		seedConfig(t, c)

		root, _ := newRootCmd()
		out, err := runCmd(t, root, "combine", "--timeout", "200ms", "--explain",
			"orders=.[] | select(.vip)", "--with", "$orders | length")
		require.NoError(t, err)
		require.Contains(t, out, "cross-source combine")
		require.Contains(t, out, "$orders  <-  orders (mongo)")
		require.Contains(t, out, "| length")
	})
}

func TestBuildCombinePlanPerStageAndFinal(t *testing.T) {
	cfg := &config{}
	stages := []combineStage{
		{varName: "orders", spec: sourceSpec{endpoint: endpoint{handle: "orders", url: "mongodb://localhost:27017/shop", driver: "mongo"}, filter: ".[] | select(.total > 10)"}},
		{varName: "cache", spec: sourceSpec{endpoint: endpoint{handle: "cache", url: "redis://localhost:6379", driver: "redis"}, filter: ".[]"}},
	}
	out, err := buildCombinePlan(cfg, stages, "$orders + $cache | length", false)
	require.NoError(t, err)
	require.Contains(t, out, "cross-source combine")
	require.Contains(t, out, "$orders  <-  orders (mongo)")
	require.Contains(t, out, `"$gt": 10`)
	require.Contains(t, out, "$cache  <-  cache (redis)")
	require.Contains(t, out, "SCAN 0 MATCH *")
	require.Contains(t, out, "combine (over the bound $vars")
	require.Contains(t, out, "| length")
}

// TestBuildCombinePlanShowsTheWrite pins that an --explain of a writing combine
// names its destination and the driver's write operations. Without it the plan
// renders as though the command only reads, which is the one thing an explain of
// a destructive run must not do. The write mode reaches the driver too, so
// --no-overwrite is visible in the plan rather than only at the write boundary.
func TestBuildCombinePlanShowsTheWrite(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("joined", "redis://h:6379/1"))
	seedConfig(t, c)

	stages := []combineStage{
		{varName: "orders", spec: sourceSpec{endpoint: endpoint{handle: "orders", url: "redis://h:6379/0", driver: "redis"}, filter: ".[]"}},
	}

	t.Run("upsert names the destination and its ops", func(t *testing.T) {
		out, err := buildCombinePlan(&config{insert: "joined"}, stages, "$orders[]", false)
		require.NoError(t, err)
		require.Contains(t, out, "write  ->  joined")
		require.Contains(t, out, "JSON.SET")
	})

	t.Run("--no-overwrite reaches the driver's write plan", func(t *testing.T) {
		out, err := buildCombinePlan(&config{insert: "joined", noOverwrite: true}, stages, "$orders[]", false)
		require.NoError(t, err)
		require.Contains(t, out, "write  ->  joined")
		require.NotContains(t, out, "(replace)", "insert-only must not plan a replacing write")
	})

	t.Run("no destination, no write section", func(t *testing.T) {
		out, err := buildCombinePlan(&config{}, stages, "$orders[]", false)
		require.NoError(t, err)
		require.NotContains(t, out, "write  ->")
	})

	t.Run("an unknown destination surfaces", func(t *testing.T) {
		_, err := buildCombinePlan(&config{insert: "nope"}, stages, "$orders[]", false)
		require.Error(t, err)
	})

	// The plan has to refuse exactly what the run refuses. A file:// source is
	// read-only — the file driver registers no write describer — so a plan that
	// rendered a write section for one would describe a run that cannot happen,
	// and would have no operations to list under it either.
	t.Run("a read-only destination is refused, as the run refuses it", func(t *testing.T) {
		dump := filepath.Join(t.TempDir(), "dump.jsonl")
		require.NoError(t, os.WriteFile(dump, []byte("{\"key\":\"k\",\"value\":1}\n"), 0o600))
		c := newSeed()
		require.NoError(t, c.Add("joined", "redis://h:6379/1"))
		require.NoError(t, c.Add("dump", "file://"+dump))
		seedConfig(t, c)

		_, err := buildCombinePlan(&config{insert: "dump"}, stages, "$orders[]", false)
		require.ErrorContains(t, err, "cannot be written to")
	})

	t.Run("stdout is refused", func(t *testing.T) {
		_, err := buildCombinePlan(&config{insert: "-"}, stages, "$orders[]", false)
		require.ErrorContains(t, err, "must name a saved source")
	})
}

func TestBuildCombinePlanDescribedAnnotatesStages(t *testing.T) {
	// The combine plan routes both the per-stage reducer and the final combine program
	// through the same annotated renderer when describe is set.
	cfg := &config{}
	stages := []combineStage{
		{varName: "orders", spec: sourceSpec{endpoint: endpoint{handle: "orders", url: "mongodb://localhost:27017/shop", driver: "mongo"}, filter: ".[] | select(.total > 10)"}},
	}
	out, err := buildCombinePlan(cfg, stages, "$orders | length", true)
	require.NoError(t, err)
	// The reducer's root .[] stage is marked with its route (a mongo scan streams);
	// its select stays a dimmed note, and the source-less combine program is dimmed too.
	require.Contains(t, out, "— each element (streaming scan)")
	require.Contains(t, out, "— keep inputs where .total > 10")
	require.Contains(t, out, "— length")
	// The final combine program runs over null input, not a store, so it carries no
	// route mark even though it is annotated — its markable flag is false.
	require.Equal(t, 1, strings.Count(out, "streaming scan"), "only the reducer stage is a marked scan")
	require.NotContains(t, out, "materialized scan")
}

func TestClassifyAccess(t *testing.T) {
	// The selector's route classification maps to one of three named costs; --unbounded
	// downgrades a streamable scan to a materialized one.
	tests := []struct {
		name      string
		filter    string
		unbounded bool
		label     string
	}{
		{"keyed field is a bounded read", ".foo", false, "bounded read"},
		{"leading key then iterate stays bounded", ".users[]", false, "bounded read"},
		{"root iterate is a streaming scan", ".[] | select(.n > 1)", false, "streaming scan"},
		{"aggregate over the root materializes", "keys", false, "materialized scan"},
		{"identity materializes", ".", false, "materialized scan"},
		{"unbounded downgrades a stream to materialized", ".[]", true, "materialized scan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := gojq.Parse(tt.filter)
			require.NoError(t, err)
			label, c := classifyAccess(selector.Keys(q), tt.unbounded)
			require.Equal(t, tt.label, label)
			require.NotNil(t, c)
		})
	}
}

func TestBuildJQPlanAccessMarkColorsRootStage(t *testing.T) {
	// The root stage's note leads with its route name in the route color; other stages
	// stay dimmed. Color is forced on so the escape wrapping is asserted, not just text.
	restore := color.NoColor
	color.NoColor = false
	defer func() { color.NoColor = restore }()

	cfg := &config{url: "redis://localhost:6379", handle: "cache"}
	out, err := buildJQPlan(cfg, ".foo", false, true)
	require.NoError(t, err)
	require.Contains(t, out, pal.ok.Sprint("(bounded read)"))
}

// mustParseCmd parses a filter for a unit test that calls a query-based helper.
func mustParseCmd(t *testing.T, filter string) *gojq.Query {
	t.Helper()
	q, err := gojq.Parse(filter)
	require.NoError(t, err)
	return q
}

func TestBuildJQPlanCrossSourceHasNoRouteMark(t *testing.T) {
	// A cross-source filter reads through source(), so no single store owns it: the
	// annotated form still describes the stages but never tags a data-access route.
	cfg := &config{}
	out, err := buildJQPlan(cfg, `source("orders"; ".[]") | length`, true, true)
	require.NoError(t, err)
	require.NotContains(t, out, "bounded read")
	require.NotContains(t, out, "streaming scan")
	require.NotContains(t, out, "materialized scan")
}

func TestBuildJQPlanFuncDefMarksBodyStage(t *testing.T) {
	// A leading func-def is stage 0; the route mark must land on the first body stage
	// (the .foo read), never on the declaration line.
	cfg := &config{url: "redis://localhost:6379", handle: "cache"}
	out, err := buildJQPlan(cfg, "def f: .+1; .foo | f", false, true)
	require.NoError(t, err)
	require.Contains(t, out, "— field .foo (bounded read)")
	require.NotContains(t, out, "def f: . + 1; ") // no note appended to the decl line
}

func TestBuildJQPlanMarkedStageWithoutDescription(t *testing.T) {
	// `.a and .b` is a bounded read whose stage has no confident jq description, so its
	// note is the bare colored route with no parenthesized-description form.
	cfg := &config{url: "redis://localhost:6379", handle: "cache"}
	out, err := buildJQPlan(cfg, ".a and .b", false, true)
	require.NoError(t, err)
	require.Contains(t, out, "— bounded read")
	require.NotContains(t, out, "(bounded read)")
}

func TestWriteConjunctLine(t *testing.T) {
	// The decision rows align: a "pushed" label is padded to the width of the
	// longer "client-side" so the conjunct sources line up in a column, and a
	// client-side conjunct trails its reason after an em dash. Color is off under
	// `go test` (stdout is not a terminal), so the exact bytes are asserted.
	tests := []struct {
		name   string
		pushed bool
		expr   string
		reason string
		want   string
	}{
		{
			name:   "pushed label is padded to the client-side column",
			pushed: true,
			expr:   ".total > 99",
			want:   "  pushed       .total > 99\n",
		},
		{
			name:   "client-side label trails its reason",
			pushed: false,
			expr:   ".count > 5",
			reason: "declined by backend translator",
			want:   "  client-side  .count > 5  — declined by backend translator\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			writeConjunctLine(&b, tt.pushed, tt.expr, tt.reason)
			require.Equal(t, tt.want, b.String())
		})
	}
}

// planLogRecord builds a sourcePlan through the shared assembly, emits it as the
// "query plan" record, and returns the parsed JSON so a test asserts the exact
// structured attrs — the same data the pretty --explain text is built from.
func planLogRecord(t *testing.T, url, handle, filter string, compile bool) map[string]any {
	t.Helper()
	sp, err := buildSourcePlan(url, filter, compile, false)
	require.NoError(t, err)
	require.True(t, sp.hasPlan)

	var buf bytes.Buffer
	lg := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	lg.Info("query plan", sp.logAttrs(handle)...)

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	require.Equal(t, "query plan", rec["msg"])
	return rec
}

func TestQueryPlanLogAttrs(t *testing.T) {
	t.Run("mongo pushes a range conjunct, shares the pretty fixture", func(t *testing.T) {
		// The same fixture as TestBuildJQPlanSingleSourceMongo, asserted structurally.
		rec := planLogRecord(t, "mongodb://h/shop", "orders", ".[] | select(.total > 99) | {id, total}", true)
		require.Equal(t, "orders", rec["handle"])
		require.Equal(t, "mongo", rec["driver"])

		cls := rec["classification"].(map[string]any)
		require.Equal(t, true, cls["scan"])
		require.Equal(t, true, cls["streamable"])
		require.Nil(t, cls["keys"])

		ops := rec["ops"].([]any)
		require.NotEmpty(t, ops)

		// The pushed server-side filter is a nested object (slog.Any), CI-parseable.
		filter := rec["filter"].(map[string]any)
		require.NotEmpty(t, filter)

		conj := rec["conjuncts"].([]any)
		require.Len(t, conj, 1)
		c0 := conj[0].(map[string]any)
		require.Equal(t, ".total > 99", c0["expr"])
		require.Equal(t, true, c0["pushed"])
		require.NotContains(t, c0, "reason") // omitempty when pushed
	})

	t.Run("redis client-side conjunct carries its reason and no filter", func(t *testing.T) {
		rec := planLogRecord(t, "redis://h", "cache", ".[] | select(.active)", true)
		require.Equal(t, "redis", rec["driver"])
		require.NotContains(t, rec, "filter") // no server-side filter -> attr omitted

		c0 := rec["conjuncts"].([]any)[0].(map[string]any)
		require.Equal(t, false, c0["pushed"])
		require.Equal(t, "not a pushable comparison", c0["reason"])
	})

	t.Run("bounded key fetch classifies keys, no scan, no conjuncts attr", func(t *testing.T) {
		rec := planLogRecord(t, "redis://h", "cache", `.["user:1"]`, true)
		cls := rec["classification"].(map[string]any)
		require.Equal(t, false, cls["scan"])
		require.Equal(t, []any{"user:1"}, cls["keys"])
		// A bounded fetch has no select conjuncts, so the conjuncts attr is omitted
		// entirely rather than emitted empty.
		require.NotContains(t, rec, "conjuncts")
		require.NotContains(t, rec, "filter")
	})
}

// TestLogQueryPlanEmitsRecord pins that logQueryPlan emits the "query plan" record
// for a resolved single-source query when an INFO sink is listening, and that the
// caller's (non-nil) context reaches the Enabled gate.
func TestLogQueryPlanEmitsRecord(t *testing.T) {
	var buf bytes.Buffer
	// gateHandler enables only for a non-nil context, so a substituted nil ctx in
	// the Enabled gate would drop the record.
	lg := slog.New(&gateHandler{inner: slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})})
	// logStructured true: the record is gated on the structured sink being active.
	cfg := &config{url: "redis://h", handle: "cache", logger: lg, logStructured: true}

	cfg.logQueryPlan(".[] | select(.active)", false)

	rec := map[string]any{}
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec))
	require.Equal(t, "query plan", rec["msg"])
	require.Equal(t, "cache", rec["handle"])
	require.Equal(t, "redis", rec["driver"])
}

// TestLogQueryPlanSkipsWhenNoInfoSink pins that the plan is not built (no record)
// when the structured sink is active but below INFO — the cheap no-op path.
func TestLogQueryPlanSkipsWhenNoInfoSink(t *testing.T) {
	var buf bytes.Buffer
	lg := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := &config{url: "redis://h", handle: "cache", logger: lg, logStructured: true}
	cfg.logQueryPlan(".[] | select(.active)", false)
	require.Empty(t, buf.String(), "a structured sink below INFO must emit no plan record")
}

// TestLogQueryPlanSkipsWithoutStructuredSink pins the FIX-1 gate: even with an INFO
// sink listening (e.g. a bare -v tinted sink), no "query plan" record is emitted
// when the structured sink is off, so the pretty plan text is never duplicated as a
// tinted one-liner. Removing the cfg.logStructured guard would emit the record here.
func TestLogQueryPlanSkipsWithoutStructuredSink(t *testing.T) {
	var buf bytes.Buffer
	lg := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := &config{url: "redis://h", handle: "cache", logger: lg, logStructured: false}
	cfg.logQueryPlan(".[] | select(.active)", false)
	require.Empty(t, buf.String(), "no structured sink must emit no plan record even when INFO is enabled")
}

// TestLogQueryPlanSkipsUnknownScheme pins that logQueryPlan emits no record when the
// source has no registered driver (buildSourcePlan returns hasPlan false with no
// error): the guard must require BOTH no error AND a plan before logging, so a
// bare-error or a bare-hasPlan condition would wrongly log an empty plan.
func TestLogQueryPlanSkipsUnknownScheme(t *testing.T) {
	var buf bytes.Buffer
	lg := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := &config{url: "postgres://h/db", handle: "x", logger: lg, logStructured: true}
	cfg.logQueryPlan(".[]", false)
	require.Empty(t, buf.String(), "an unknown scheme has no plan, so no query plan record is emitted")
}

// TestBuildSourcePlanDriverWithoutDescriber pins the `d.explainPlan == nil` guard:
// a registered driver with no describer must yield hasPlan false rather than
// dereferencing a nil describer. No shipped driver reaches this branch, so it is
// exercised by injecting a describer-less driver into the registry for the test.
func TestBuildSourcePlanDriverWithoutDescriber(t *testing.T) {
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	// A driver with a scheme but a nil explainPlan (the zero-value field).
	drivers = append(append([]driver{}, orig...), driver{name: "nulldrv", schemes: []string{"nulldrv"}})

	sp, err := buildSourcePlan("nulldrv://x", ".[]", true, false)
	require.NoError(t, err)
	require.False(t, sp.hasPlan, "a driver without a describer contributes no plan (no nil-deref)")
}

func TestQueryPlanLogSharesConjunctDecisionsWithText(t *testing.T) {
	// The log record's per-conjunct pushed/reason must equal the pretty text's
	// decisions for the same filter — one assembly, no drift.
	cfg := &config{url: "mongodb://h/shop", handle: "orders"}
	filter := ".[] | select(.total > 99 and (.active | not))"
	out, err := buildJQPlan(cfg, filter, false, false)
	require.NoError(t, err)

	rec := planLogRecord(t, cfg.url, cfg.handle, filter, true)
	conj := rec["conjuncts"].([]any)
	require.Len(t, conj, 2)

	pushed := conj[0].(map[string]any)
	require.Equal(t, ".total > 99", pushed["expr"])
	require.Equal(t, true, pushed["pushed"])
	require.Contains(t, out, ".total > 99")

	declined := conj[1].(map[string]any)
	require.Equal(t, ".active | not", declined["expr"])
	require.Equal(t, false, declined["pushed"])
	require.Equal(t, "negation is not exactly expressible", declined["reason"])
	require.Contains(t, out, "negation is not exactly expressible")
}

func TestBuildJQPlanPushdownDecisions(t *testing.T) {
	// The assertions match plain text, so pin color off regardless of what an
	// earlier test left in the global mode.
	orig := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = orig })
	// The pushdown section enumerates, per top-level select conjunct, whether the
	// backend evaluates it (pushed) or it re-runs client-side, with the reason a
	// client-side conjunct did not push. Compile-stage declines carry a precise
	// reason; a compiled conjunct a backend cannot express is declined generically;
	// a driver with no scan-side filter reports every conjunct client-side.
	tests := []struct {
		name        string
		url         string
		handle      string
		filter      string
		contains    []string
		notContains []string
	}{
		{
			name:     "mongo pushes a range conjunct",
			url:      "mongodb://h/shop",
			handle:   "orders",
			filter:   `.[] | select(.total > 99)`,
			contains: []string{"pushdown:", "pushed", ".total > 99", `"$gt": 99`},
		},
		{
			name:     "mongo declines a negation at compile stage",
			url:      "mongodb://h/shop",
			handle:   "orders",
			filter:   `.[] | select(.total > 99 and (.active | not))`,
			contains: []string{"pushed", ".total > 99", "client-side", ".active | not", "negation is not exactly expressible"},
		},
		{
			name:        "mongo declines a non-portable regex at compile stage",
			url:         "mongodb://h/shop",
			handle:      "orders",
			filter:      `.[] | select(.name | test("(?i)x"))`,
			contains:    []string{"client-side", `.name | test("(?i)x")`, "regex is not portable across backends"},
			notContains: []string{"  filter:"},
		},
		{
			name:        "mongo names an unsafe field at compile stage",
			url:         "mongodb://h/shop",
			handle:      "orders",
			filter:      `.[] | select(.["a.b"] == 5)`,
			contains:    []string{"client-side", "a.b", "unsafe to push"},
			notContains: []string{"  filter:"},
		},
		{
			name:        "elasticsearch declines a range at the backend translator",
			url:         "elasticsearch://h/idx",
			handle:      "logs",
			filter:      `.[] | select(.total > 99)`,
			contains:    []string{"pushdown:", "client-side", ".total > 99", "declined by backend translator"},
			notContains: []string{"  filter:"},
		},
		{
			name:        "file has no server-side filter",
			url:         "file:///tmp/dump.json",
			handle:      "dump",
			filter:      `.[] | select(.a == 1)`,
			contains:    []string{"pushdown:", "client-side", ".a == 1", "store applies no server-side filter"},
			notContains: []string{"  filter:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config{url: tt.url, handle: tt.handle}
			out, err := buildJQPlan(cfg, tt.filter, false, false)
			require.NoError(t, err)
			for _, s := range tt.contains {
				require.Contains(t, out, s)
			}
			for _, s := range tt.notContains {
				require.NotContains(t, out, s)
			}
		})
	}
}

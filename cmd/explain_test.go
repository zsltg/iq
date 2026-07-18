package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

func TestBuildJQPlanSingleSourceMongo(t *testing.T) {
	// A resolved single-source plan pretty-prints the filter and, with pushdown on
	// by default, shows the pushed MongoDB filter as the backend call.
	cfg := &config{url: "mongodb://localhost:27017/shop", handle: "orders"}
	out, err := buildJQPlan(cfg, ".[] | select(.total > 99) | {id, total}", false)
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

func TestBuildJQPlanMongoNoCompileNoFilter(t *testing.T) {
	// With --no-compile the plan shows a full-collection scan and no pushed filter,
	// and pushdown is off entirely, so there is no per-conjunct breakdown either.
	cfg := &config{url: "mongodb://localhost:27017/shop", handle: "orders", noCompile: true}
	out, err := buildJQPlan(cfg, ".[] | select(.total > 99)", false)
	require.NoError(t, err)
	require.Contains(t, out, "full-collection scan")
	require.NotContains(t, out, "  filter:")
	require.NotContains(t, out, "pushdown:")
}

func TestBuildJQPlanRedisScan(t *testing.T) {
	cfg := &config{url: "redis://localhost:6379", handle: "cache"}
	out, err := buildJQPlan(cfg, ".[] | select(.active)", false)
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
	out, err := buildJQPlan(cfg, `source("orders"; ".[] | select(.vip)") | length`, true)
	require.NoError(t, err)
	require.Contains(t, out, "cross-source")
	require.Contains(t, out, `source("orders";`)
	require.Contains(t, out, "select(.vip)")
	require.NotContains(t, out, "calls:") // no backend section for cross-source
}

func TestBuildJQPlanReportsSyntaxError(t *testing.T) {
	cfg := &config{url: "redis://localhost:6379", handle: "cache"}
	_, err := buildJQPlan(cfg, ".[ | broken", false)
	require.Error(t, err)
}

func TestWriteAccessPlanUnknownSchemeIsSilent(t *testing.T) {
	// An unrecognized scheme (no registered driver) contributes no backend section.
	var b strings.Builder
	require.NoError(t, writeAccessPlan(&b, "postgres://x/y", ".[]", false, false))
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
		out, err := runCmd(t, root, "--timeout", "200ms", "--explain",
			"--from", "orders=.[] | select(.vip)", "--combine", "$orders | length")
		require.NoError(t, err)
		require.Contains(t, out, "cross-source combine")
		require.Contains(t, out, "$orders  <-  orders (mongo)")
		require.Contains(t, out, "| length")
	})
}

func TestBuildCombinePlanPerStageAndFinal(t *testing.T) {
	cfg := &config{
		combine: "$orders + $cache | length",
	}
	stages := []fromStage{
		{varName: "orders", handle: "orders", source: iqconfig.Source{URL: "mongodb://localhost:27017/shop"}, filter: ".[] | select(.total > 10)"},
		{varName: "cache", handle: "cache", source: iqconfig.Source{URL: "redis://localhost:6379"}, filter: ".[]"},
	}
	out, err := buildCombinePlan(cfg, stages)
	require.NoError(t, err)
	require.Contains(t, out, "cross-source combine")
	require.Contains(t, out, "$orders  <-  orders (mongo)")
	require.Contains(t, out, `"$gt": 10`)
	require.Contains(t, out, "$cache  <-  cache (redis)")
	require.Contains(t, out, "SCAN 0 MATCH *")
	require.Contains(t, out, "combine (over the bound $vars")
	require.Contains(t, out, "| length")
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

func TestBuildJQPlanPushdownDecisions(t *testing.T) {
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
			out, err := buildJQPlan(cfg, tt.filter, false)
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

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
	// With --no-compile the plan shows a full-collection scan and no pushed filter.
	cfg := &config{url: "mongodb://localhost:27017/shop", handle: "orders", noCompile: true}
	out, err := buildJQPlan(cfg, ".[] | select(.total > 99)", false)
	require.NoError(t, err)
	require.Contains(t, out, "full-collection scan")
	require.NotContains(t, out, "  filter:")
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

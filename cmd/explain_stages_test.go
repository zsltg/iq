package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// noColor pins plain text output for the test, whatever an earlier test left in
// the global color mode.
func noColor(t *testing.T) {
	t.Helper()
	orig := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = orig })
}

// notePad pads left to the note column and adds the section indent, so a test row
// reads the way writeNoteRow writes it. Tests that pin the exact bytes of a whole
// plan do not use it.
func notePad(indent int, left, note string) string {
	pad := max(noteColumn-(indent-sectionIndent)-len(left), 2)
	return strings.Repeat(" ", indent) + left + strings.Repeat(" ", pad) + "— " + note
}

// requireLinesInOrder checks that every wanted line appears in out as a whole line,
// in the given order, with other lines allowed between them.
func requireLinesInOrder(t *testing.T, out string, want []string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	i := 0
	for _, w := range want {
		for i < len(lines) && lines[i] != w {
			i++
		}
		require.Lessf(t, i, len(lines), "line %q is missing or out of order in:\n%s", w, out)
		i++
	}
}

const stageFilter = `.[] | select(.n == 1 and .t > 3 and (.name | ascii_downcase) == "x")`

func TestBuildJQPlanStageLayout(t *testing.T) {
	noColor(t)
	const jqHeader = "jq (after decode, the full filter, every conjunct again):"
	const notCompiled = `  only here: (.name | ascii_downcase) == "x"  — not compiled: not a pushable comparison`
	tests := []struct {
		name        string
		url         string
		filter      string
		unbounded   bool
		noCompile   bool
		lines       []string
		notContains []string
	}{
		{
			name:   "redis: no server stage, prefilter and jq only",
			url:    "redis://h",
			filter: stageFilter,
			lines: []string{
				"server filter: none, Redis filters nothing on the server",
				"prefilter (rawpred on the raw bytes of RedisJSON values, before decode):",
				"  and",
				notePad(4, "n == 1", ".n == 1"),
				notePad(4, "t > 3", ".t > 3"),
				jqHeader,
				notCompiled,
			},
			notContains: []string{"not pushed", "pushdown:"},
		},
		{
			name:   "elasticsearch: conditional server, declined range stays in the prefilter",
			url:    "elasticsearch://h/idx",
			filter: stageFilter,
			lines: []string{
				"server filter (elasticsearch bool query, if each equality field is exactly mapped and its literal type matches):",
				notePad(2, `{"term": {"n": 1}}`, ".n == 1"),
				"prefilter (rawpred on the raw _source of each hit, before decode):",
				"  and",
				notePad(4, "n == 1", ".n == 1"),
				notePad(4, "t > 3", ".t > 3") + "  (not pushed: declined by backend translator)",
				jqHeader,
				notCompiled,
			},
		},
		{
			name:   "cassandra: conditional server, no prefilter, declined conjuncts only in jq",
			url:    "cassandra://h/ks?table=t",
			filter: stageFilter,
			lines: []string{
				`server filter (cql where, if each referenced column exists and its type accepts the literal):`,
				notePad(2, `{"where": "\"n\" = ?"}`, ".n == 1"),
				jqHeader,
				notePad(2, "only here: .t > 3", "not pushed: declined by backend translator"),
				notCompiled,
			},
			notContains: []string{"prefilter"},
		},
		{
			name:   "mongo: a wide fragment prints as indented JSON with the note on the brace",
			url:    "mongodb://h/shop",
			filter: `.[] | select(.year > 2015)`,
			lines: []string{
				"server filter (mongo find):",
				notePad(2, "{", ".year > 2015"),
				`    "$or": [`,
				"      {",
				`        "$or": [`,
				`          {"year": {"$gt": 2015}},`,
				`          {"year": {"$type": ["string", "array", "object"]}}`,
				"        ]",
				"      },",
				`      {"year": {"$type": "decimal"}}`,
				"    ]",
				"  }",
				"jq (after decode, the full filter, every conjunct again)",
			},
			notContains: []string{"prefilter", "only here"},
		},
		{
			name:   "mongo: a narrow fragment prints on one line",
			url:    "mongodb://h/shop",
			filter: `.[] | select(.status == "a" or .status == "b")`,
			lines: []string{
				"server filter (mongo find):",
				notePad(2, `{"status": {"$in": ["a", "b"]}}`, `.status == "a" or .status == "b"`),
				"jq (after decode, the full filter, every conjunct again)",
			},
		},
		{
			name:   "file: no server, conditional prefilter",
			url:    "file:///tmp/dump.jsonl",
			filter: stageFilter,
			lines: []string{
				"server filter: none, a dump file has no server",
				"prefilter (only for an uncached typed-JSONL dump: rawpred on the raw value of each record, before decode):",
				"  and",
				notePad(4, "n == 1", ".n == 1"),
				jqHeader,
				notCompiled,
			},
		},
		{
			name:   "couchbase: an exact WHERE turns the prefilter off",
			url:    "couchbase://h/?bucket=b",
			filter: `.[] | select(.n == 1)`,
			lines: []string{
				"prefilter: off, the WHERE captures the predicate exactly",
				"jq (after decode, the full filter, every conjunct again)",
			},
			notContains: []string{"only here"},
		},
		{
			name:   "elasticsearch: an _id predicate turns the prefilter off",
			url:    "elasticsearch://h/idx",
			filter: `.[] | select(._id == "a" and .t > 1)`,
			lines: []string{
				"prefilter: off, the predicate reads _id, which the raw _source does not carry",
				jqHeader,
				notePad(2, "only here: .t > 1", "not pushed: declined by backend translator"),
			},
		},
		{
			name:   "redis: a widened or renders as a tree with the note on its operator",
			url:    "redis://h",
			filter: `.[] | select((.a == 1 and (.b | ascii_downcase) == "x") or .c == 2)`,
			lines: []string{
				"prefilter (rawpred on the raw bytes of RedisJSON values, before decode):",
				notePad(2, "or", `(.a == 1 and (.b | ascii_downcase) == "x") or .c == 2`),
				"    a == 1",
				"    c == 2",
				"jq (after decode, the full filter, every conjunct again)",
			},
			notContains: []string{"  and\n"},
		},
		{
			name:   "redis: nested and and or nest the same way",
			url:    "redis://h",
			filter: `.[] | select((.a == 1 and .b == 2) or (.c == 2 and (.d == 4 or .e == 5)))`,
			lines: []string{
				notePad(2, "or", "(.a == 1 and .b == 2) or (.c == 2 and (.d == 4 or .e == 5))"),
				"    and",
				"      a == 1",
				"      b == 2",
				"    and",
				"      c == 2",
				"      or",
				"        d == 4",
				"        e == 5",
			},
		},
		{
			name:      "materialized route: jq rows only",
			url:       "mongodb://h/shop",
			filter:    `.[] | select(.total > 99 and .n == 1)`,
			unbounded: true,
			lines: []string{
				jqHeader,
				notePad(2, "only here: .total > 99", materializedReason),
				notePad(2, "only here: .n == 1", materializedReason),
			},
			notContains: []string{"server filter", "prefilter"},
		},
		{
			name:      "bounded read keeps its key fetch block",
			url:       "elasticsearch://h/idx",
			filter:    `.a, .b`,
			unbounded: true,
			lines: []string{
				"server filter (key fetch):",
				`  {"ids": ["a", "b"]}`,
			},
			notContains: []string{"jq (after decode", "prefilter"},
		},
		{
			name:      "no-compile: no stage sections",
			url:       "mongodb://h/shop",
			filter:    `.[] | select(.total > 99)`,
			noCompile: true,
			notContains: []string{
				"server filter", "prefilter", "jq (after decode", "only here",
			},
		},
		{
			name:      "no-compile bounded read keeps its key fetch block",
			url:       "elasticsearch://h/idx",
			filter:    `.a, .b`,
			noCompile: true,
			lines:     []string{"server filter (key fetch):", `  {"ids": ["a", "b"]}`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config{url: tt.url, handle: "src", unbounded: tt.unbounded, noCompile: tt.noCompile}
			out, err := buildJQPlan(cfg, tt.filter, false, false)
			require.NoError(t, err)
			requireLinesInOrder(t, out, tt.lines)
			for _, s := range tt.notContains {
				require.NotContains(t, out, s)
			}
		})
	}
}

func TestBuildJQPlanStageSectionOrder(t *testing.T) {
	noColor(t)
	out, err := buildJQPlan(&config{url: "elasticsearch://h/idx", handle: "src"}, stageFilter, false, false)
	require.NoError(t, err)
	last := -1
	for _, section := range []string{"elasticsearch calls:", "server filter (", "prefilter (", "jq (after decode"} {
		i := strings.Index(out, "\n"+section)
		require.Greater(t, i, last, "section %q is missing or out of order", section)
		last = i
	}
	require.NotContains(t, out, "pushdown:")
	require.NotContains(t, out, "  filter:")
}

// TestBuildJQPlanStageExactRedis pins every byte of a Redis plan, so the note
// column cannot drift.
func TestBuildJQPlanStageExactRedis(t *testing.T) {
	noColor(t)
	out, err := buildJQPlan(&config{url: "redis://h", handle: "src"}, stageFilter, false, false)
	require.NoError(t, err)
	want := `query plan
source: src (redis)

jq filter:
  .[]
  | select(
    .n == 1 and .t > 3 and (
      .name
      | ascii_downcase
    ) == "x"
  )

redis calls:
  SCAN 0 MATCH * COUNT 100: cursor over the whole keyspace
  per page: pipeline TYPE for the page's keys
  then per key by type: GET / HLEN + HSCAN / LLEN + LRANGE / SCARD + SSCAN / ZCARD + ZSCAN / XLEN + XRANGE COUNT / JSON.GET

server filter: none, Redis filters nothing on the server

prefilter (rawpred on the raw bytes of RedisJSON values, before decode):
  and
    n == 1                          — .n == 1
    t > 3                           — .t > 3

jq (after decode, the full filter, every conjunct again):
  only here: (.name | ascii_downcase) == "x"  — not compiled: not a pushable comparison
`
	require.Equal(t, want, out)
}

// TestBuildJQPlanStageExactElasticsearch pins every byte of an Elasticsearch plan.
func TestBuildJQPlanStageExactElasticsearch(t *testing.T) {
	noColor(t)
	out, err := buildJQPlan(&config{url: "elasticsearch://h/idx", handle: "src"}, stageFilter, false, false)
	require.NoError(t, err)
	want := `query plan
source: src (elasticsearch)

jq filter:
  .[]
  | select(
    .n == 1 and .t > 3 and (
      .name
      | ascii_downcase
    ) == "x"
  )

elasticsearch calls:
  _search(<bool>): server-side pre-filter (equality on an exactly-mapped field and existence), then the full jq re-runs client-side
  pages streamed in batches of 100

server filter (elasticsearch bool query, if each equality field is exactly mapped and its literal type matches):
  {"term": {"n": 1}}                — .n == 1

prefilter (rawpred on the raw _source of each hit, before decode):
  and
    n == 1                          — .n == 1
    t > 3                           — .t > 3  (not pushed: declined by backend translator)

jq (after decode, the full filter, every conjunct again):
  only here: (.name | ascii_downcase) == "x"  — not compiled: not a pushable comparison
`
	require.Equal(t, want, out)
}

func TestBuildJQPlanStageColorKeepsAlignment(t *testing.T) {
	// With color on, the notes are faint and the section keys are colored. The
	// padding comes from the plain left text, so the note starts in the same column.
	orig := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = orig })
	out, err := buildJQPlan(&config{url: "redis://h", handle: "src"}, stageFilter, false, false)
	require.NoError(t, err)
	require.Contains(t, out, "  and\n    n == 1                          "+pal.faint.Sprint("— .n == 1")+"\n")
	require.Contains(t, out, pal.header.Sprint("server filter:")+" none, ")
}

// planOf returns the source plan of a filter, failing the test on an error.
func planOf(t *testing.T, url, filter string, unbounded bool) sourcePlan {
	t.Helper()
	sp, err := buildSourcePlan(planSource{url: url}, filter, true, unbounded)
	require.NoError(t, err)
	require.True(t, sp.hasPlan)
	return sp
}

// conjunctsJSON is the JSON of the conjunct plans, the form the log record and the
// MCP result carry.
func conjunctsJSON(t *testing.T, cs []conjunctPlan) string {
	t.Helper()
	raw, err := json.Marshal(cs)
	require.NoError(t, err)
	return string(raw)
}

func TestSourcePlanStructured(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		filter       string
		unbounded    bool
		wantServer   string
		wantPrefilt  string
		wantFilter   bool
		wantConjunct string
	}{
		{
			name:        "elasticsearch: server conditional, prefilter on, three conjunct shapes",
			url:         "elasticsearch://h/idx",
			filter:      stageFilter,
			wantServer:  `{"state":"conditional","scope":"elasticsearch bool query","note":"each equality field is exactly mapped and its literal type matches"}`,
			wantPrefilt: `{"state":"on","scope":"rawpred on the raw _source of each hit, before decode"}`,
			wantFilter:  true,
			wantConjunct: `[
				{"expr":".n == 1","compiled":"n == 1","server":{"term":{"n":1}},"stages":["server","prefilter","jq"]},
				{"expr":".t > 3","compiled":"t > 3","stages":["prefilter","jq"],"skipped":{"server":"declined by backend translator"}},
				{"expr":"(.name | ascii_downcase) == \"x\"","stages":["jq"],"reason":"not compiled: not a pushable comparison"}
			]`,
		},
		{
			name:        "redis: no server stage, so no skipped server",
			url:         "redis://h",
			filter:      `.[] | select(.n == 1)`,
			wantServer:  `{"state":"none","note":"Redis filters nothing on the server"}`,
			wantPrefilt: `{"state":"on","scope":"rawpred on the raw bytes of RedisJSON values, before decode"}`,
			wantConjunct: `[
				{"expr":".n == 1","compiled":"n == 1","stages":["prefilter","jq"]}
			]`,
		},
		{
			name:        "mongo: no prefilter stage, so no skipped prefilter",
			url:         "mongodb://h/shop",
			filter:      `.[] | select(.total > 99)`,
			wantServer:  `{"state":"on","scope":"mongo find"}`,
			wantPrefilt: `{"state":"none"}`,
			wantFilter:  true,
			wantConjunct: `[
				{"expr":".total > 99","compiled":"total > 99","server":{"$or":[{"$or":[{"total":{"$gt":99}},{"total":{"$type":["string","array","object"]}}]},{"total":{"$type":"decimal"}}]},"stages":["server","jq"]}
			]`,
		},
		{
			name:        "couchbase: an exact WHERE skips the prefilter with its note",
			url:         "couchbase://h/?bucket=b",
			filter:      `.[] | select(.n == 1)`,
			wantServer:  `{"state":"on","scope":"sql++ where"}`,
			wantPrefilt: `{"state":"off","note":"the WHERE captures the predicate exactly"}`,
			wantFilter:  true,
			wantConjunct: `[
				{"expr":".n == 1","compiled":"n == 1","server":{"params":{"p0":1},"where":"` + "`t`.`n` = $p0" + `"},"stages":["server","jq"],"skipped":{"prefilter":"the WHERE captures the predicate exactly"}}
			]`,
		},
		{
			name:        "cassandra: a range alone leaves the server stage off",
			url:         "cassandra://h/ks?table=t",
			filter:      `.[] | select(.t > 3)`,
			wantServer:  `{"state":"off","note":"no conjunct pushes"}`,
			wantPrefilt: `{"state":"none"}`,
			wantConjunct: `[
				{"expr":".t > 3","compiled":"t > 3","stages":["jq"],"skipped":{"server":"declined by backend translator"}}
			]`,
		},
		{
			name:      "materialized: jq only with the reason, no stage plans",
			url:       "mongodb://h/shop",
			filter:    `.[] | select(.total > 99)`,
			unbounded: true,
			wantConjunct: `[
				{"expr":".total > 99","stages":["jq"],"reason":"materialized: the scan reads every record"}
			]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := planOf(t, tt.url, tt.filter, tt.unbounded)
			require.JSONEq(t, tt.wantConjunct, conjunctsJSON(t, sp.conjuncts))
			require.Equal(t, tt.wantFilter, sp.filter != nil)

			attrs := logAttrMap(t, sp)
			out, err := explainResult(&config{url: tt.url, handle: "src", unbounded: tt.unbounded}, tt.filter, "plan")
			require.NoError(t, err)
			raw, err := json.Marshal(out)
			require.NoError(t, err)
			var res map[string]any
			require.NoError(t, json.Unmarshal(raw, &res))

			if tt.wantServer == "" {
				for _, got := range []map[string]any{attrs, res} {
					require.NotContains(t, got, "server")
					require.NotContains(t, got, "prefilter")
				}
				return
			}
			for _, got := range []map[string]any{attrs, res} {
				gotServer, err := json.Marshal(got["server"])
				require.NoError(t, err)
				require.JSONEq(t, tt.wantServer, string(gotServer))
				gotPre, err := json.Marshal(got["prefilter"])
				require.NoError(t, err)
				require.JSONEq(t, tt.wantPrefilt, string(gotPre))
				gotConj, err := json.Marshal(got["conjuncts"])
				require.NoError(t, err)
				require.JSONEq(t, tt.wantConjunct, string(gotConj))
			}
		})
	}
}

// logAttrMap emits the plan as a JSON log record and returns it parsed, so a test
// sees the attributes as a log reader does.
func logAttrMap(t *testing.T, sp sourcePlan) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	lg := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	lg.Info("query plan", sp.logAttrs("src")...)
	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	return rec
}

func TestSourcePlanMaterializedOmitsStagePlans(t *testing.T) {
	sp := planOf(t, "mongodb://h/shop", `.[] | select(.total > 99)`, true)
	require.Nil(t, sp.filter)
	rec := logAttrMap(t, sp)
	require.NotContains(t, rec, "server")
	require.NotContains(t, rec, "prefilter")
	require.NotContains(t, rec, "filter")
	require.Contains(t, rec, "conjuncts")
}

func TestExplainResultElasticsearchStagePlans(t *testing.T) {
	out, err := explainResult(&config{url: "elasticsearch://h/idx", handle: "logs"}, stageFilter, "plan")
	require.NoError(t, err)
	require.NotNil(t, out.Server)
	require.Equal(t, "conditional", out.Server.State)
	require.NotNil(t, out.Prefilter)
	require.Equal(t, "on", out.Prefilter.State)
	require.Len(t, out.Conjuncts, 3)
	require.Equal(t, []string{"server", "prefilter", "jq"}, out.Conjuncts[0].Stages)
	require.Equal(t, map[string]string{"server": declinedReason}, out.Conjuncts[1].Skipped)
	require.Equal(t, "not compiled: not a pushable comparison", out.Conjuncts[2].Reason)
}

// TestExplainResultBoundedReadHasNoStagePlans pins that the MCP result names no
// server or prefilter stage for a bounded read, even if the driver has the stages.
func TestExplainResultBoundedReadHasNoStagePlans(t *testing.T) {
	out, err := explainResult(&config{url: "elasticsearch://h/idx", handle: "logs"}, `.a, .b`, "plan")
	require.NoError(t, err)
	require.False(t, out.Classification.Scan)
	require.Nil(t, out.Server)
	require.Nil(t, out.Prefilter)
}

func TestBuildSourcePlanKeepsNoStagesOnBoundedRead(t *testing.T) {
	sp := planOf(t, "elasticsearch://h/idx", `.a, .b`, false)
	require.NotNil(t, sp.filter)
	require.Empty(t, sp.conjuncts)
	require.Equal(t, query.StageNone, sp.server.State)
	require.Equal(t, query.StageNone, sp.prefilter.State)
}

// hbaseBadURI has a ?types= entry that the HBase URI parser rejects.
const hbaseBadURI = "hbase://zk/?table=t&types=noequals"

const hbaseGoodURI = "hbase://zk/?table=t&types=cf:a=int"

const hbaseFilter = `.[] | select(.cf.a == 1)`

// failHBaseOpen replaces the HBase opener with one that fails the test. A planning
// error stops before any open, so the opener never runs.
func failHBaseOpen(t *testing.T) {
	t.Helper()
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	swapped := append([]driver{}, orig...)
	found := false
	for i := range swapped {
		if swapped[i].name == "hbase" {
			swapped[i].open = func(context.Context, *config) (store, error) {
				t.Fatal("the planning error must stop before the HBase opener runs")
				return nil, nil
			}
			found = true
		}
	}
	require.True(t, found, "the hbase driver is registered")
	drivers = swapped
}

func TestPlanErrorTextPaths(t *testing.T) {
	const parseErr = `malformed ?types= entry "noequals"`
	c := newSeed()
	require.NoError(t, c.Add("bad", hbaseBadURI))
	seedConfig(t, c)
	failHBaseOpen(t)

	tests := []struct {
		name string
		args []string
	}{
		{"iq explain", []string{"--src", "bad", "--timeout", "200ms", "--explain", hbaseFilter}},
		{"iq verbose", []string{"--src", "bad", "--timeout", "200ms", "-v", hbaseFilter}},
		{"combine explain", []string{"combine", "--timeout", "200ms", "--explain", "bad=" + hbaseFilter, "--with", "$bad | length"}},
		{"combine verbose", []string{"combine", "--timeout", "200ms", "-v", "bad=" + hbaseFilter, "--with", "$bad | length"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := newRootCmd()
			_, err := runCmd(t, root, tt.args...)
			require.ErrorContains(t, err, parseErr)
		})
	}

	t.Run("mcp iq_explain", func(t *testing.T) {
		cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)
		res := callMCP(t, cs, "iq_explain", map[string]any{"source": "bad", "filter": hbaseFilter})
		require.True(t, res.IsError)
		require.Contains(t, contentText(res), "malformed ?types= entry")
		require.Contains(t, contentText(res), "noequals")
	})
}

// hbaseSlashURI saves a table with a slash. The HBase parser rejects it unless an
// address replaces the table.
const hbaseSlashURI = "hbase://zk/?table=bad/name&types=cf:a=int"

func TestPlanUsesAddress(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("books", hbaseSlashURI))
	seedConfig(t, c)
	failHBaseOpen(t)

	tests := []struct {
		name string
		args []string
	}{
		{"iq explain", []string{"--src", "books.good", "--timeout", "200ms", "--explain", hbaseFilter}},
		{"iq verbose explain", []string{"--src", "books.good", "--timeout", "200ms", "-v", "--explain", hbaseFilter}},
		{"combine explain", []string{"combine", "--timeout", "200ms", "--explain", "books.good=" + hbaseFilter, "--with", "$books_good | length"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := newRootCmd()
			out, err := runCmd(t, root, tt.args...)
			require.NoError(t, err)
			require.Contains(t, out, "SingleColumnValueFilter")
		})
	}

	t.Run("mcp iq_explain", func(t *testing.T) {
		cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)
		res := callMCP(t, cs, "iq_explain", map[string]any{"source": "books.good", "filter": hbaseFilter})
		require.False(t, res.IsError, contentText(res))
		require.Contains(t, contentText(res), "SingleColumnValueFilter")
	})

	t.Run("explainResult", func(t *testing.T) {
		cfg := &config{url: hbaseSlashURI, address: "good", handle: "books"}
		out, err := explainResult(cfg, hbaseFilter, "plan")
		require.NoError(t, err)
		require.Equal(t, "hbase", out.Driver)
	})

	t.Run("query plan record", func(t *testing.T) {
		var buf bytes.Buffer
		lg := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		cfg := &config{url: hbaseSlashURI, address: "good", handle: "books", logger: lg, logStructured: true}
		cfg.logQueryPlan(hbaseFilter, false)
		rec := parseLogRecords(t, buf.String())["query plan"]
		require.NotNil(t, rec)
		require.Equal(t, "hbase", rec["driver"])
	})
}

func TestPlanErrorRecordHelpers(t *testing.T) {
	newCfg := func(buf *bytes.Buffer) *config {
		lg := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		return &config{handle: "hb", logger: lg, logStructured: true}
	}
	tests := []struct {
		name   string
		uri    string
		call   func(cfg *config)
		wantOK bool
	}{
		{"query plan, bad uri", hbaseBadURI, func(cfg *config) { cfg.logQueryPlan(hbaseFilter, false) }, false},
		{"query plan, valid uri", hbaseGoodURI, func(cfg *config) { cfg.logQueryPlan(hbaseFilter, false) }, true},
		{"stage plan, bad uri", hbaseBadURI, func(cfg *config) { cfg.logStagePlan(cfg.planSource(), "hb", hbaseFilter) }, false},
		{"stage plan, valid uri", hbaseGoodURI, func(cfg *config) { cfg.logStagePlan(cfg.planSource(), "hb", hbaseFilter) }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cfg := newCfg(&buf)
			cfg.url = tt.uri
			tt.call(cfg)
			if !tt.wantOK {
				require.Empty(t, buf.String(), "a planning error writes no record")
				return
			}
			rec := parseLogRecords(t, buf.String())["query plan"]
			require.NotNil(t, rec)
			require.Equal(t, "hbase", rec["driver"])
		})
	}
}

func TestPlanErrorExecutionWithLogAlone(t *testing.T) {
	// The run path plans nothing to print, so the real HBase opener runs. The URI
	// parser rejects the URI before it creates a client.
	c := newSeed()
	require.NoError(t, c.Add("bad", hbaseBadURI))
	seedConfig(t, c)

	root, _ := newRootCmd()
	out, err := runCmd(t, root, "--src", "bad", "--timeout", "2s", "--log.file=stderr", "--log.format=json", hbaseFilter)
	require.ErrorContains(t, err, `malformed ?types= entry "noequals"`)
	require.NotContains(t, out, `"query plan"`)
}

func TestFitJSON(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		indent int
		width  int
		want   []string
	}{
		{"scalar", "x", 2, 36, []string{`  "x"`}},
		{"long scalar never breaks", strings.Repeat("a", 50), 0, 10, []string{`"` + strings.Repeat("a", 50) + `"`}},
		{"short object", map[string]any{"term": map[string]any{"n": 1}}, 2, 36, []string{`  {"term": {"n": 1}}`}},
		{
			"nested object that fits",
			map[string]any{"a": map[string]any{"b": []any{"x", "y"}}},
			0, 100,
			[]string{`{"a": {"b": ["x", "y"]}}`},
		},
		{
			"mongo range",
			map[string]any{"$or": []any{
				map[string]any{"year": map[string]any{"$gt": 2015}},
				map[string]any{"year": map[string]any{"$type": []string{"string", "array", "object"}}},
			}},
			2, 36,
			[]string{
				`  {`,
				`    "$or": [`,
				`      {"year": {"$gt": 2015}},`,
				`      {"year": {"$type": ["string", "array", "object"]}}`,
				`    ]`,
				`  }`,
			},
		},
		{
			"array that breaks",
			[]string{"alpha", "beta", "gamma"},
			0, 10,
			[]string{`[`, `  "alpha",`, `  "beta",`, `  "gamma"`, `]`},
		},
		{"empty object", map[string]any{}, 0, 36, []string{`{}`}},
		{"empty array", []any{}, 0, 36, []string{`[]`}},
		{"html and quote stay literal", map[string]any{"s": `a<b>"c"&`}, 0, 100, []string{`{"s": "a<b>\"c\"&"}`}},
		{"keys sorted", map[string]any{"b": 1, "a": 2, "c": nil}, 0, 100, []string{`{"a": 2, "b": 1, "c": null}`}},
		{"whole number and float", []any{1.0, 2.5, true}, 0, 100, []string{`[1, 2.5, true]`}},
		{"top level fits exactly", []string{"abc"}, 2, 9, []string{`  ["abc"]`}},
		{"top level one column too wide", []string{"abc"}, 2, 8, []string{`  [`, `    "abc"`, `  ]`}},
		{
			"member line of exactly 80 columns stays compact",
			map[string]any{"a": []string{strings.Repeat("x", 68)}, "b": 1},
			0, 10,
			[]string{`{`, `  "a": ["` + strings.Repeat("x", 68) + `"],`, `  "b": 1`, `}`},
		},
		{
			"member line of 81 columns breaks",
			map[string]any{"a": []string{strings.Repeat("x", 69)}, "b": 1},
			0, 10,
			[]string{`{`, `  "a": [`, `    "` + strings.Repeat("x", 69) + `"`, `  ],`, `  "b": 1`, `}`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fitJSON(tt.value, tt.indent, tt.width)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFitJSONRejectsAnUnencodableValue(t *testing.T) {
	_, err := fitJSON(map[string]any{"n": math.NaN()}, 2, 36)
	var unsupported *json.UnsupportedValueError
	require.ErrorAs(t, err, &unsupported)
}

func TestJSONString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", `""`},
		{"plain", "abc", `"abc"`},
		{"quote", `a"b`, `"a\"b"`},
		{"backslash", `a\b`, `"a\\b"`},
		{"newline", "a\nb", `"a\nb"`},
		{"carriage return", "a\rb", `"a\rb"`},
		{"tab", "a\tb", `"a\tb"`},
		{"backspace", "a\bb", `"a\bb"`},
		{"form feed", "a\fb", `"a\fb"`},
		{"line separator", "a\u2028b", `"a\u2028b"`},
		{"paragraph separator", "a\u2029b", `"a\u2029b"`},
		{"control rune without a short form", "a\x01b\x1f", `"a\u0001b\u001f"`},
		{"space is not a control rune", "a b", `"a b"`},
		{"html stays literal", "<a>&", `"<a>&"`},
		{"non-ASCII stays literal", "é世", `"é世"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, jsonString(tt.in))
			var back string
			require.NoError(t, json.Unmarshal([]byte(jsonString(tt.in)), &back))
			require.Equal(t, tt.in, back, "the literal decodes to the input")
		})
	}
}

// errStageDown is the error of the fake planner.
var errStageDown = errors.New("planner down")

// useStageDriver registers a driver with the scheme "stg" and the given planner for
// the test.
func useStageDriver(t *testing.T, plan func(pred predicate.Node, unbounded bool) (query.AccessPlan, error)) {
	t.Helper()
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	drivers = append(append([]driver{}, orig...), driver{
		name:    "stg",
		schemes: []string{"stg"},
		explainPlan: func(_ planSource, _ selector.KeySet, pred predicate.Node, unbounded bool) (query.AccessPlan, error) {
			return plan(pred, unbounded)
		},
	})
}

var stageServerOn = query.StagePlan{State: query.StageOn, Scope: "fake"}

func TestBuildSourcePlanWrapsAWholePredicateError(t *testing.T) {
	useStageDriver(t, func(predicate.Node, bool) (query.AccessPlan, error) {
		return query.AccessPlan{}, errStageDown
	})
	_, err := buildSourcePlan(planSource{url: "stg://x"}, stageFilterTwo, true, false)
	require.ErrorIs(t, err, errStageDown)
	require.ErrorContains(t, err, "plan stg source")
}

const stageFilterTwo = `.[] | select(.a == 1 and .b == 2)`

func TestBuildSourcePlanReturnsAConjunctPlanError(t *testing.T) {
	// The whole predicate (an and node) plans well. One conjunct fails.
	useStageDriver(t, func(pred predicate.Node, _ bool) (query.AccessPlan, error) {
		if _, whole := pred.(predicate.And); whole {
			return query.AccessPlan{Server: stageServerOn}, nil
		}
		return query.AccessPlan{}, errStageDown
	})
	sp, err := buildSourcePlan(planSource{url: "stg://x"}, stageFilterTwo, true, false)
	require.ErrorIs(t, err, errStageDown)
	require.ErrorContains(t, err, "plan stg conjunct")
	require.Empty(t, sp.conjuncts)
}

func TestBuildSourcePlanPlansConjunctsAsBounded(t *testing.T) {
	// The fake returns a fragment only for a read that is not unbounded. A conjunct
	// plan on the streaming route must ask as a bounded read.
	useStageDriver(t, func(_ predicate.Node, unbounded bool) (query.AccessPlan, error) {
		ap := query.AccessPlan{Server: stageServerOn}
		if !unbounded {
			ap.Filter = map[string]any{"fragment": 1}
		}
		return ap, nil
	})
	sp, err := buildSourcePlan(planSource{url: "stg://x"}, `.[] | select(.a == 1)`, true, false)
	require.NoError(t, err)
	require.Len(t, sp.conjuncts, 1)
	require.Equal(t, map[string]any{"fragment": 1}, sp.conjuncts[0].Server)
	require.Equal(t, []string{"server", "jq"}, sp.conjuncts[0].Stages)
}

// fragmentOf is a conjunct plan with a server fragment.
func fragmentOf(expr string, frag map[string]any) conjunctPlan {
	return conjunctPlan{Expr: expr, Server: frag, Stages: []string{stageServer, stageJQ}}
}

func TestWriteStageSectionsServerRows(t *testing.T) {
	noColor(t)
	jqOnly := conjunctPlan{Expr: ".c == 3", Stages: []string{stageJQ}}
	tests := []struct {
		name   string
		server query.StagePlan
		rows   []conjunctPlan
		want   []string
		absent []string
	}{
		{
			name:   "a conjunct without a fragment does not stop the next rows",
			server: stageServerOn,
			rows:   []conjunctPlan{jqOnly, fragmentOf(".a == 1", map[string]any{"a": 1}), fragmentOf(".b == 2", map[string]any{"b": 2})},
			want: []string{
				notePad(2, `{"a": 1}`, ".a == 1"),
				notePad(2, `{"b": 2}`, ".b == 2"),
			},
		},
		{
			name:   "an off stage writes no rows",
			server: query.StagePlan{State: query.StageOff, Note: "no conjunct pushes"},
			rows:   []conjunctPlan{fragmentOf(".a == 1", map[string]any{"a": 1})},
			want:   []string{"server filter: off, no conjunct pushes"},
			absent: []string{`{"a": 1}`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			require.NoError(t, writeStageSections(&b, sourcePlan{server: tt.server, conjuncts: tt.rows}))
			for _, w := range tt.want {
				require.Contains(t, b.String(), w)
			}
			for _, a := range tt.absent {
				require.NotContains(t, b.String(), a)
			}
		})
	}
}

func TestWriteStageSectionsOffPrefilterWritesNoRows(t *testing.T) {
	noColor(t)
	row := conjunctPlan{
		Expr:   ".a == 1",
		Stages: []string{stagePrefilter, stageJQ},
		node:   predicate.Eq{Path: []string{"a"}, Value: 1.0},
	}
	var b strings.Builder
	sp := sourcePlan{
		server:    query.StagePlan{State: query.StageNone, Note: "no server"},
		prefilter: query.StagePlan{State: query.StageOff, Note: "nothing to scan"},
		conjuncts: []conjunctPlan{row},
	}
	require.NoError(t, writeStageSections(&b, sp))
	require.Contains(t, b.String(), "off, nothing to scan")
	require.NotContains(t, b.String(), "— .a == 1")
}

func TestWriteStageSectionsKeyFetchWidth(t *testing.T) {
	noColor(t)
	// The compact form is 34 characters, which fits the 36 columns with the indent.
	fits := map[string]any{"k": strings.Repeat("a", 25)}
	tooWide := map[string]any{"k": strings.Repeat("a", 26)}
	tests := []struct {
		name   string
		filter map[string]any
		want   []string
	}{
		{"fits on one line", fits, []string{`  {"k": "` + strings.Repeat("a", 25) + `"}`}},
		{"one column too wide breaks", tooWide, []string{`  {`, `    "k": "` + strings.Repeat("a", 26) + `"`, `  }`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			require.NoError(t, writeStageSections(&b, sourcePlan{filter: tt.filter, keys: selector.KeySet{Keys: []string{"a"}}}))
			requireLinesInOrder(t, b.String(), tt.want)
		})
	}
}

func TestWriteStageSectionsRejectsAnUnencodableFilter(t *testing.T) {
	bad := map[string]any{"n": math.NaN()}
	tests := []struct {
		name string
		sp   sourcePlan
		want string
	}{
		{
			"server fragment",
			sourcePlan{server: stageServerOn, conjuncts: []conjunctPlan{fragmentOf(".n == 1", bad)}},
			"render server fragment",
		},
		{
			"second server fragment",
			sourcePlan{server: stageServerOn, conjuncts: []conjunctPlan{
				fragmentOf(".a == 1", map[string]any{"a": 1}), fragmentOf(".n == 1", bad),
			}},
			"render server fragment",
		},
		{"key fetch filter", sourcePlan{filter: bad}, "render server filter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			err := writeStageSections(&b, tt.sp)
			var unsupported *json.UnsupportedValueError
			require.ErrorAs(t, err, &unsupported)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

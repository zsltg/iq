package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqfile "github.com/zsltg/iq/drivers/file"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// mcpDump is the seeded keyspace every unit test reads: a typed JSONL dump, the
// same record shape a `--typed` export writes, so a file:// source serves it with
// no container.
const mcpDump = `{"key":"alpha","value":{"id":"alpha","status":"new","total":150}}
{"key":"beta","value":{"id":"beta","status":"done","total":10}}
{"key":"gamma","value":{"id":"gamma","status":"new","total":99}}
`

// seedMCP writes the dump, points IQ_CONFIG at a fresh registry, and registers
// the dump as the `snap` source (also the active one). It returns the dump path
// so a test can register a second source against the same data.
func seedMCP(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	data := filepath.Join(dir, "data.jsonl")
	require.NoError(t, os.WriteFile(data, []byte(mcpDump), 0o600))
	c := newSeed()
	require.NoError(t, c.Add("snap", iqfile.URL(data)))
	require.NoError(t, c.SetActive("snap"))
	t.Setenv(iqconfig.EnvConfig, filepath.Join(dir, "iq.toml"))
	require.NoError(t, c.Save())
	return data
}

// newTestMCPServer builds the server under test with the given --allow set and
// the caps a test wants, at a timeout long enough for a local dump decode.
func newTestMCPServer(allow []string, maxItems, maxBytes int) *mcpServer {
	allowed := map[string]bool{}
	for _, a := range allow {
		allowed[a] = true
	}
	return &mcpServer{
		cfg:      &config{timeout: 30 * time.Second, logger: slog.New(slog.DiscardHandler)},
		maxItems: maxItems,
		maxBytes: maxBytes,
		allow:    allowed,
	}
}

// connectMCP wires a client to the server over the SDK's in-memory transports and
// returns the live session, closed on cleanup.
func connectMCP(t *testing.T, s *mcpServer, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	return connectMCPServer(t, s.newServer(), opts)
}

// connectMCPServer connects a client to an already-built server, so a test that
// needs to shape the server (a middleware, a downgraded protocol) can.
func connectMCPServer(t *testing.T, srv *mcp.Server, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(t.Context(), st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, opts).Connect(t.Context(), ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callMCP calls one tool and returns the result, failing on a protocol error (a
// tool-level failure comes back as a result with IsError, not as an error).
func callMCP(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

// structOf decodes a successful result's structuredContent into v, failing when
// the call errored.
func structOf(t *testing.T, res *mcp.CallToolResult, v any) {
	t.Helper()
	require.Falsef(t, res.IsError, "tool failed: %s", contentText(res))
	require.NotNil(t, res.StructuredContent)
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, v))
}

// contentText joins a result's text content blocks, the rendering a client shows.
func contentText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// errorBodyOf decodes an error result's JSON error document, asserting the result
// is an error and carries the CLI's shape.
func errorBodyOf(t *testing.T, res *mcp.CallToolResult) errorJSON {
	t.Helper()
	require.True(t, res.IsError, "expected an error result")
	var body errorJSON
	require.NoErrorf(t, json.Unmarshal([]byte(contentText(res)), &body), "error content is not the JSON error shape: %s", contentText(res))
	return body
}

// toolByName finds one advertised tool.
func toolByName(t *testing.T, cs *mcp.ClientSession, name string) *mcp.Tool {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	for _, tool := range res.Tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not advertised", name)
	return nil
}

func TestMCPQueryReadsAndStreams(t *testing.T) {
	tests := []struct {
		name      string
		args      map[string]any
		wantCount int
		wantFirst string
	}{
		{
			name:      "bounded read by key",
			args:      map[string]any{"source": "snap", "filter": `.["alpha"]`},
			wantCount: 1,
			wantFirst: "alpha",
		},
		{
			name:      "streamed scan",
			args:      map[string]any{"source": "snap", "filter": ".[]"},
			wantCount: 3,
		},
		{
			name:      "streamed scan with a pushable select",
			args:      map[string]any{"source": "snap", "filter": `.[] | select(.status == "new") | .id`},
			wantCount: 2,
		},
		{
			name:      "holistic filter with unbounded",
			args:      map[string]any{"source": "snap", "filter": "length", "unbounded": true},
			wantCount: 1,
		},
		{
			name:      "no source falls back to the active one",
			args:      map[string]any{"filter": `.["beta"].id`},
			wantCount: 1,
			wantFirst: "beta",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seedMCP(t)
			cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

			var out mcpQueryOutput
			structOf(t, callMCP(t, cs, "iq_query", tc.args), &out)
			require.Equal(t, tc.wantCount, out.Count)
			require.Len(t, out.Items, tc.wantCount)
			require.False(t, out.Truncated)
			if tc.wantFirst != "" {
				require.Contains(t, fmt.Sprint(out.Items[0]), tc.wantFirst)
			}
		})
	}
}

func TestMCPQueryTruncates(t *testing.T) {
	tests := []struct {
		name      string
		serverMax [2]int // items, bytes
		args      map[string]any
		wantCount int
	}{
		{
			name:      "server max-items cuts the scan short",
			serverMax: [2]int{2, 256 * 1024},
			args:      map[string]any{"source": "snap", "filter": ".[]"},
			wantCount: 2,
		},
		{
			name:      "per-call max_items lowers the server cap",
			serverMax: [2]int{200, 256 * 1024},
			args:      map[string]any{"source": "snap", "filter": ".[]", "max_items": 1},
			wantCount: 1,
		},
		{
			name:      "per-call max_items cannot raise the server cap",
			serverMax: [2]int{1, 256 * 1024},
			args:      map[string]any{"source": "snap", "filter": ".[]", "max_items": 100},
			wantCount: 1,
		},
		{
			name:      "server max-bytes cuts the scan short",
			serverMax: [2]int{200, 45},
			args:      map[string]any{"source": "snap", "filter": ".[]"},
			wantCount: 1,
		},
		{
			name:      "per-call max_bytes lowers the server cap",
			serverMax: [2]int{200, 256 * 1024},
			args:      map[string]any{"source": "snap", "filter": ".[]", "max_bytes": 45},
			wantCount: 1,
		},
		{
			name:      "a first item over the byte cap truncates to nothing",
			serverMax: [2]int{200, 4},
			args:      map[string]any{"source": "snap", "filter": ".[]"},
			wantCount: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seedMCP(t)
			cs := connectMCP(t, newTestMCPServer(nil, tc.serverMax[0], tc.serverMax[1]), nil)

			var out mcpQueryOutput
			structOf(t, callMCP(t, cs, "iq_query", tc.args), &out)
			require.True(t, out.Truncated, "expected the result to be marked truncated")
			require.Equal(t, tc.wantCount, out.Count)
			require.Len(t, out.Items, tc.wantCount)
		})
	}
}

func TestMCPQueryRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"holistic filter without unbounded", map[string]any{"source": "snap", "filter": "length"}, "unbounded"},
		{"syntax error", map[string]any{"source": "snap", "filter": ".["}, "unexpected"},
		{"unknown source", map[string]any{"source": "nope", "filter": ".[]"}, `unknown source "nope"`},
		{"empty filter", map[string]any{"source": "snap", "filter": "  "}, "a filter is required"},
		{"negative max_items", map[string]any{"source": "snap", "filter": ".[]", "max_items": -1}, "invalid max_items"},
		{"negative max_bytes", map[string]any{"source": "snap", "filter": ".[]", "max_bytes": -1}, "invalid max_bytes"},
		{"unparseable timeout", map[string]any{"source": "snap", "filter": ".[]", "timeout": "soon"}, "want a duration such as"},
		{"non-positive timeout", map[string]any{"source": "snap", "filter": ".[]", "timeout": "0s"}, "invalid timeout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seedMCP(t)
			cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

			body := errorBodyOf(t, callMCP(t, cs, "iq_query", tc.args))
			require.Contains(t, body.Error.Message, tc.want)
		})
	}
}

// TestMCPErrorRedactsPassword proves a stored credential never reaches a client:
// the source URI carries one, the driver's open failure echoes it, and every
// string in the rendered error is redacted.
func TestMCPErrorRedactsPassword(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("secret", "redis://user:hunter2@127.0.0.1:1/0"))
	seedConfig(t, c)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	res := callMCP(t, cs, "iq_query", map[string]any{
		"source": "secret", "filter": ".[]", "timeout": "1s",
	})
	body := errorBodyOf(t, res)
	require.NotContains(t, contentText(res), "hunter2")
	require.NotContains(t, body.Error.Message, "hunter2")
	for _, cause := range body.Error.Causes {
		require.NotContains(t, cause, "hunter2")
	}
}

func TestMCPExplainStructuredPlan(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	var out mcpExplainOutput
	structOf(t, callMCP(t, cs, "iq_explain", map[string]any{
		"source": "snap", "filter": `.[] | select(.status == "new")`,
	}), &out)

	require.Equal(t, "snap", out.Handle)
	require.Equal(t, "file", out.Driver)
	require.True(t, out.Classification.Scan)
	require.True(t, out.Classification.Streamable)
	require.Empty(t, out.Classification.Keys)
	require.NotEmpty(t, out.Ops)
	require.Contains(t, out.Plan, "query plan")
	// Every pipe stage is described in the plan text, as `--explain -v` does.
	require.Contains(t, out.Plan, "keep inputs where")
	require.NotContains(t, out.Plan, "\x1b[", "plan text must render with color off")
}

// TestMCPExplainCompilesByDefault proves the pushdown compile is on unless the
// call turns it off: a Mongo plan (no connection is made) reports the pushed
// conjuncts and the server-side filter by default, and neither without compile.
func TestMCPExplainCompilesByDefault(t *testing.T) {
	seedMCP(t)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("shop", "mongodb://h/db?collection=orders"))
	require.NoError(t, cf.Save())
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)
	filter := `.[] | select(.status == "new")`

	var compiled mcpExplainOutput
	structOf(t, callMCP(t, cs, "iq_explain", map[string]any{"source": "shop", "filter": filter}), &compiled)
	require.NotEmpty(t, compiled.Conjuncts)
	require.NotEmpty(t, compiled.Filter)

	var plain mcpExplainOutput
	structOf(t, callMCP(t, cs, "iq_explain", map[string]any{"source": "shop", "filter": filter, "compile": false}), &plain)
	require.Empty(t, plain.Conjuncts)
	require.Empty(t, plain.Filter)
}

// TestMCPSourcesListsVerboseWithoutExpanding proves the listing is the verbose
// projection (options included) over the saved URIs: a keyring-backed source
// shows its stored URI, never the password the keyring would inject.
func TestMCPSourcesListsVerboseWithoutExpanding(t *testing.T) {
	seedMCP(t)
	fk := useFakeKeyring(t)
	require.NoError(t, fk.Set("kr", "hunter2"))
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("opt", "redis://h:6379/0"))
	require.NoError(t, cf.SetOption("@opt", "timeout", "30s"))
	cf.Sources["kr"] = iqconfig.Source{URL: "redis://u@h:6379/0", Keyring: true}
	require.NoError(t, cf.Save())
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	var out mcpSourcesOutput
	structOf(t, callMCP(t, cs, "iq_sources", nil), &out)
	byHandle := map[string]sourceRow{}
	for _, r := range out.Sources {
		byHandle[r.Handle] = r
	}
	require.Equal(t, "30s", byHandle["opt"].Options["timeout"])
	require.True(t, byHandle["kr"].Keyring)
	require.Equal(t, "redis://u@h:6379/0", byHandle["kr"].Location)
}

// TestMCPInspectLiveSource drives iq_inspect against a reachable MongoDB: the
// result is the JSON rendering keyed by section, narrowed by only, never the text
// or YAML forms or the --list summary.
func TestMCPInspectLiveSource(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable MongoDB")
	}
	u := os.Getenv("IQ_MONGO_URL")
	if u == "" {
		u = "mongodb://localhost:27017/iq"
	}
	seedMCP(t)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("live", u+"?collection=books"))
	require.NoError(t, cf.Save())
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	res := callMCP(t, cs, "iq_inspect", map[string]any{"source": "live", "only": []string{"dbStats"}})
	require.False(t, res.IsError, contentText(res))
	sections, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "expected an object keyed by section")
	require.Contains(t, sections, "dbStats")
	require.NotContains(t, sections, "buildInfo")
	stats, ok := sections["dbStats"].(map[string]any)
	require.True(t, ok, "dbStats must be the command's document, not a rendering: %T", sections["dbStats"])
	require.Contains(t, stats, "db")
}

// TestMCPProgressReportsEstimate proves the progress a client sees carries the
// store's estimated total when the driver can count, so a client can render a
// fraction and not only a running count.
func TestMCPProgressReportsEstimate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable MongoDB")
	}
	u := os.Getenv("IQ_MONGO_URL")
	if u == "" {
		u = "mongodb://localhost:27017/iq"
	}
	seedMongo(t, u, "mcp_progress", []string{`{"_id":"a","n":1}`, `{"_id":"b","n":2}`})
	seedMCP(t)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("live", u+"?collection=mcp_progress"))
	require.NoError(t, cf.Save())

	totals := make(chan float64, 16)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			select {
			case totals <- req.Params.Total:
			default:
			}
		},
	})
	params := &mcp.CallToolParams{Name: "iq_query", Arguments: map[string]any{"source": "live", "filter": ".[] | .n"}}
	params.SetProgressToken("scan-live")
	res, err := cs.CallTool(t.Context(), params)
	require.NoError(t, err)
	require.False(t, res.IsError, contentText(res))

	deadline := time.After(5 * time.Second)
	for {
		select {
		case total := <-totals:
			if total == 2 {
				return
			}
		case <-deadline:
			t.Fatal("no progress notification carried the estimated total")
		}
	}
}

// TestMCPCrossSource proves a filter that reads entirely through source() runs
// and explains without a primary source, and that its plan still validates
// against the declared output schema.
func TestMCPCrossSource(t *testing.T) {
	t.Run("query reads through source()", func(t *testing.T) {
		seedMCP(t)
		cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

		var out mcpQueryOutput
		structOf(t, callMCP(t, cs, "iq_query", map[string]any{
			"filter": `source("snap"; ".[] | .id")`,
		}), &out)
		require.Equal(t, 3, out.Count)
	})

	t.Run("a syntax error keeps its position without a primary source", func(t *testing.T) {
		seedMCP(t)
		cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

		body := errorBodyOf(t, callMCP(t, cs, "iq_query", map[string]any{
			"filter": `source("snap"; ".[]") | .[`,
		}))
		require.Contains(t, body.Error.Message, "unexpected")
		require.NotNil(t, body.Error.Offset, "the parse error must stay unwrappable with no URI to redact: %+v", body.Error)
		require.Positive(t, *body.Error.Offset)
	})

	t.Run("explain names no driver and stays schema-valid", func(t *testing.T) {
		seedMCP(t)
		cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

		tool := toolByName(t, cs, "iq_explain")
		raw, err := json.Marshal(tool.OutputSchema)
		require.NoError(t, err)
		var sch jsonschema.Schema
		require.NoError(t, json.Unmarshal(raw, &sch))
		resolved, err := sch.Resolve(nil)
		require.NoError(t, err)

		res := callMCP(t, cs, "iq_explain", map[string]any{"filter": `source("snap"; ".[]")`})
		require.Falsef(t, res.IsError, "explain failed: %s", contentText(res))
		require.NoError(t, resolved.Validate(res.StructuredContent))

		var out mcpExplainOutput
		structOf(t, res, &out)
		require.Empty(t, out.Driver)
		require.Empty(t, out.Ops)
		require.NotNil(t, out.Ops, "an always-present array must never serialize as null")
		require.NotNil(t, out.Classification.Keys)
		require.Contains(t, out.Plan, "cross-source")
	})
}

// TestMCPRefusesStdoutLogSink proves the one flag combination that would corrupt
// the protocol is refused before the server starts.
func TestMCPRefusesStdoutLogSink(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr bool
	}{
		{name: "--log.file stdout", args: []string{"mcp", "--log.file", "stdout"}, wantErr: true},
		{
			name:    "IQ_LOG_FILE stdout with logging on",
			args:    []string{"mcp", "--log"},
			env:     map[string]string{"IQ_LOG_FILE": "stdout"},
			wantErr: true,
		},
		{
			name: "IQ_LOG_FILE stdout without logging is inert",
			args: []string{"mcp", "--allow", "root"}, // fails later, on the capability
			env:  map[string]string{"IQ_LOG_FILE": "stdout"},
		},
		{
			name: "a log file named stderr is the stream, not a refusal",
			args: []string{"mcp", "--log.file", "stderr", "--allow", "root"},
		},
		{
			name: "a log file path is a file, not the stream",
			args: []string{"mcp", "--log.file", "LOGDIR/iq.log", "--allow", "root"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seedMCP(t)
			for i, a := range tc.args {
				tc.args[i] = strings.ReplaceAll(a, "LOGDIR", t.TempDir())
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			root, cfg := newRootCmd()
			closeResources(t, cfg)
			_, err := runCmd(t, root, tc.args...)
			require.Error(t, err)
			if tc.wantErr {
				require.ErrorContains(t, err, "cannot also log there")
				return
			}
			// The sink check passed and the run failed on the capability set,
			// which is validated after it.
			require.ErrorContains(t, err, `invalid --allow "root"`)
		})
	}
}

// TestMCPExplainKeysMatchLogAttrs pins the structured explain result to the
// fields the "query plan" log record emits, so the two renderings of one plan
// cannot drift apart.
func TestMCPExplainKeysMatchLogAttrs(t *testing.T) {
	// A Mongo plan is the one that emits every key: the file driver pushes no
	// server-side filter, so its plan would leave "filter" out.
	sp, err := buildSourcePlan("mongodb://h/db?collection=c", `.[] | select(.a == 1)`, true, false)
	require.NoError(t, err)
	require.True(t, sp.hasPlan)
	require.NotNil(t, sp.filter)
	require.NotEmpty(t, sp.conjuncts)

	want := make([]string, 0, 6)
	for _, a := range sp.logAttrs("snap") {
		attr, ok := a.(slog.Attr)
		require.True(t, ok)
		want = append(want, attr.Key)
	}
	// The plan text is the explain tool's own addition; every other key is the
	// log record's.
	// filter and conjuncts are omitempty, so a zero value omits them; they are
	// part of the contract either way.
	got := jsonFieldNames(t, mcpExplainOutput{}, "filter", "conjuncts")
	require.Contains(t, got, "plan")
	got = slices.DeleteFunc(got, func(s string) bool { return s == "plan" })
	slices.Sort(want)
	slices.Sort(got)
	require.Equal(t, want, got)
}

// jsonFieldNames returns the sorted JSON object keys a value marshals to, plus
// any extra names the caller knows are part of the contract but omitted from a
// zero value.
func jsonFieldNames(t *testing.T, v any, extra ...string) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(raw, &obj))
	names := make([]string, 0, len(obj)+len(extra))
	for k := range obj {
		names = append(names, k)
	}
	names = append(names, extra...)
	slices.Sort(names)
	return slices.Compact(names)
}

func TestMCPSourcesRedactsAndLists(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://u:hunter2@h:6379/0"))
	require.NoError(t, c.Add("prod/books", "mongodb://h/db?collection=books"))
	require.NoError(t, c.Add("prod", "mongodb://h/db"))
	require.NoError(t, c.SetActive("cache"))
	seedConfig(t, c)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	t.Run("every source, password redacted", func(t *testing.T) {
		res := callMCP(t, cs, "iq_sources", map[string]any{})
		var out mcpSourcesOutput
		structOf(t, res, &out)
		require.Len(t, out.Sources, 3)
		require.Equal(t, "cache", out.Active)
		require.NotContains(t, contentText(res), "hunter2")
		require.Contains(t, out.Sources[0].Location, "xxxxx")
	})

	t.Run("group narrows the listing", func(t *testing.T) {
		var out mcpSourcesOutput
		structOf(t, callMCP(t, cs, "iq_sources", map[string]any{"group": "prod"}), &out)
		// The group's members and a source carrying the group's own name.
		handles := make([]string, 0, len(out.Sources))
		for _, r := range out.Sources {
			handles = append(handles, r.Handle)
		}
		require.ElementsMatch(t, []string{"prod", "prod/books"}, handles)
	})
}

func TestMCPSchemaInfersShape(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	res := callMCP(t, cs, "iq_schema", map[string]any{"source": "snap"})
	require.False(t, res.IsError, contentText(res))
	sch, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "expected the schema itself as structured content")
	require.Equal(t, "object", sch["type"])
	props, ok := sch["properties"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, props, "status")
	require.Contains(t, props, "total")
}

func TestMCPDiffComparesSources(t *testing.T) {
	data := seedMCP(t)
	other := filepath.Join(filepath.Dir(data), "other.jsonl")
	// alpha changes, beta and gamma are gone, zeta is new: one delta of each op.
	require.NoError(t, os.WriteFile(other, []byte(
		`{"key":"alpha","value":{"id":"alpha","status":"done","total":151}}`+"\n"+
			`{"key":"zeta","value":{"id":"zeta","status":"new","total":1,"extra":true}}`+"\n",
	), 0o600))
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("other", iqfile.URL(other)))
	require.NoError(t, cf.Save())

	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	t.Run("identical sides do not differ", func(t *testing.T) {
		var out mcpDiffOutput
		structOf(t, callMCP(t, cs, "iq_diff", map[string]any{"a": "snap", "b": "snap"}), &out)
		require.False(t, out.Differ)
		require.Empty(t, out.Data)
	})

	t.Run("differing sides report the delta", func(t *testing.T) {
		var out mcpDiffOutput
		structOf(t, callMCP(t, cs, "iq_diff", map[string]any{"a": "snap", "b": "other"}), &out)
		require.True(t, out.Differ)
		byKey := map[string]mcpItemDelta{}
		for _, d := range out.Data {
			byKey[d.Key] = d
		}
		require.Len(t, byKey, 4)

		// A changed item names its field-level changes, each with both sides.
		alpha := byKey["alpha"]
		require.Equal(t, "change", alpha.Op)
		require.Len(t, alpha.Changes, 2, "status and total both changed: %+v", alpha.Changes)
		var status *mcpChange
		for i := range alpha.Changes {
			if slices.Contains(alpha.Changes[i].Path, "status") {
				status = &alpha.Changes[i]
			}
		}
		require.NotNil(t, status, "the status change must be reported by path: %+v", alpha.Changes)
		require.Equal(t, "change", status.Op)
		require.Equal(t, "new", status.Old)
		require.Equal(t, "done", status.New)

		// A removed item carries its old value, an added one its new value.
		require.Equal(t, "remove", byKey["beta"].Op)
		require.NotNil(t, byKey["beta"].Old)
		require.Equal(t, "remove", byKey["gamma"].Op)
		require.Equal(t, "add", byKey["zeta"].Op)
		require.NotNil(t, byKey["zeta"].New)
	})

	t.Run("a chosen layer stands alone", func(t *testing.T) {
		var out mcpDiffOutput
		structOf(t, callMCP(t, cs, "iq_diff", map[string]any{"a": "snap", "b": "other", "schema": true}), &out)
		require.True(t, out.Differ)
		require.NotEmpty(t, out.Schema)
		require.Empty(t, out.Data, "the data layer runs only by default or when asked")
	})

	t.Run("the data layer takes a filter", func(t *testing.T) {
		var out mcpDiffOutput
		structOf(t, callMCP(t, cs, "iq_diff", map[string]any{
			"a": "snap", "b": "other", "filter": `.[] | select(.id == "alpha")`,
		}), &out)
		require.True(t, out.Differ)
		require.Len(t, out.Data, 1)
		require.Equal(t, "alpha", out.Data[0].Key)
	})

	t.Run("the stats layer refuses a filter", func(t *testing.T) {
		body := errorBodyOf(t, callMCP(t, cs, "iq_diff", map[string]any{
			"a": "snap", "b": "other", "stats": true, "filter": ".[]",
		}))
		require.Contains(t, body.Error.Message, "no items to filter")
	})
}

func TestMCPInspectRefusesFileSource(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	body := errorBodyOf(t, callMCP(t, cs, "iq_inspect", map[string]any{"source": "snap"}))
	require.Contains(t, body.Error.Message, "live server metadata")
}

func TestMCPPingReportsReachability(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	var out mcpPingOutput
	structOf(t, callMCP(t, cs, "iq_ping", map[string]any{"source": "snap"}), &out)
	require.Len(t, out.Results, 1)
	require.Equal(t, "snap", out.Results[0].Handle)
	require.Equal(t, "file", out.Results[0].Driver)
	require.True(t, out.Results[0].OK, out.Results[0].Error)
}

// TestMCPToolListIsSortedAndCacheable pins the advertised list: sorted by name,
// carrying the cache hint the revision expects.
func TestMCPToolListIsSortedAndCacheable(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer(allowNames, 200, 256*1024), nil)

	res, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	require.Equal(t, []string{
		"iq_data_clear", "iq_data_delete", "iq_data_drop", "iq_diff", "iq_exec",
		"iq_explain", "iq_insert", "iq_inspect", "iq_ping", "iq_query",
		"iq_schema", "iq_sources",
	}, names)
	require.True(t, slices.IsSorted(names))
	require.Equal(t, mcpListTTLMs, res.GetTTLMs())
	require.Equal(t, "private", res.GetCacheScope())
}

// TestMCPToolsCarryTitleAndAnnotations proves every advertised tool names itself
// for a human and states all four behaviour hints explicitly, so a client never
// falls back to the spec's defaults.
func TestMCPToolsCarryTitleAndAnnotations(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer(allowNames, 200, 256*1024), nil)

	res, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Tools)
	readOnly := map[string]bool{
		"iq_sources": true, "iq_ping": true, "iq_explain": true, "iq_query": true,
		"iq_inspect": true, "iq_schema": true, "iq_diff": true,
	}
	for _, tool := range res.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			require.NotEmpty(t, tool.Title)
			require.NotEmpty(t, tool.Description)
			require.NotNil(t, tool.InputSchema)
			require.NotNil(t, tool.OutputSchema)
			a := tool.Annotations
			require.NotNil(t, a)
			require.NotEmpty(t, a.Title)
			require.NotNil(t, a.DestructiveHint)
			require.NotNil(t, a.OpenWorldHint)
			require.False(t, *a.OpenWorldHint, "every source is configured, so the world is closed")
			require.Equal(t, readOnly[tool.Name], a.ReadOnlyHint)
			require.Equal(t, !readOnly[tool.Name], *a.DestructiveHint)
			require.Equal(t, tool.Name != "iq_exec", a.IdempotentHint)
		})
	}
}

// TestMCPAllowGatesTheToolList proves --allow is the gate: a tool that is not
// allowed is never registered, so it cannot even be seen.
func TestMCPAllowGatesTheToolList(t *testing.T) {
	always := []string{"iq_diff", "iq_explain", "iq_inspect", "iq_ping", "iq_query", "iq_schema", "iq_sources"}
	tests := []struct {
		name    string
		allow   []string
		extra   []string
		absent  []string
		readErr bool
	}{
		{
			name:   "read-only by default",
			allow:  nil,
			absent: []string{"iq_insert", "iq_exec", "iq_data_clear", "iq_data_drop", "iq_data_delete"},
		},
		{
			name:   "writes adds the copy tool only",
			allow:  []string{allowWrites},
			extra:  []string{"iq_insert"},
			absent: []string{"iq_exec", "iq_data_clear", "iq_data_drop", "iq_data_delete"},
		},
		{
			name:   "exec adds the native command tool only",
			allow:  []string{allowExec},
			extra:  []string{"iq_exec"},
			absent: []string{"iq_insert", "iq_data_clear", "iq_data_drop", "iq_data_delete"},
		},
		{
			name:   "destructive adds the lifecycle tools only",
			allow:  []string{allowDestructive},
			extra:  []string{"iq_data_clear", "iq_data_delete", "iq_data_drop"},
			absent: []string{"iq_insert", "iq_exec"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seedMCP(t)
			cs := connectMCP(t, newTestMCPServer(tc.allow, 200, 256*1024), nil)

			res, err := cs.ListTools(t.Context(), nil)
			require.NoError(t, err)
			names := make([]string, 0, len(res.Tools))
			for _, tool := range res.Tools {
				names = append(names, tool.Name)
			}
			for _, want := range append(slices.Clone(always), tc.extra...) {
				require.Contains(t, names, want)
			}
			for _, absent := range tc.absent {
				require.NotContains(t, names, absent)
			}
			// An unregistered tool is a protocol error, not a tool result: the
			// server does not know the name at all.
			for _, absent := range tc.absent {
				_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: absent, Arguments: map[string]any{}})
				require.Error(t, err)
				var werr *jsonrpc.Error
				require.ErrorAs(t, err, &werr)
			}
		})
	}
}

// TestMCPStructuredContentValidatesAgainstOutputSchema runs every read tool that
// can succeed against the dump and checks its structured result against the
// output schema the SDK inferred and advertised.
func TestMCPStructuredContentValidatesAgainstOutputSchema(t *testing.T) {
	tests := []struct {
		tool string
		args map[string]any
	}{
		{"iq_sources", map[string]any{}},
		{"iq_ping", map[string]any{"source": "snap"}},
		{"iq_explain", map[string]any{"source": "snap", "filter": ".[]"}},
		{"iq_query", map[string]any{"source": "snap", "filter": ".[]"}},
		{"iq_schema", map[string]any{"source": "snap"}},
		{"iq_diff", map[string]any{"a": "snap", "b": "snap"}},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			seedMCP(t)
			cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

			tool := toolByName(t, cs, tc.tool)
			raw, err := json.Marshal(tool.OutputSchema)
			require.NoError(t, err)
			var sch jsonschema.Schema
			require.NoError(t, json.Unmarshal(raw, &sch))
			resolved, err := sch.Resolve(nil)
			require.NoError(t, err)

			res := callMCP(t, cs, tc.tool, tc.args)
			require.Falsef(t, res.IsError, "tool failed: %s", contentText(res))
			require.NoError(t, resolved.Validate(res.StructuredContent))
		})
	}
}

// TestMCPDestructiveNeedsConfirmation proves the gate on a destructive call: it
// is refused without confirm, told exactly what to pass when the client cannot
// ask its user, and proceeds once confirmed.
func TestMCPDestructiveNeedsConfirmation(t *testing.T) {
	// A file source has no container to clear, so the call fails on the
	// capability, not the confirmation. That is the point: the refusal below
	// happens before any store opens, and the confirmed call gets past it.
	t.Run("refused without confirm, and says what to pass", func(t *testing.T) {
		seedMCP(t)
		cs := connectMCP(t, newTestMCPServer([]string{allowDestructive}, 200, 256*1024), nil)

		body := errorBodyOf(t, callMCP(t, cs, "iq_data_clear", map[string]any{"source": "snap"}))
		require.Contains(t, body.Error.Message, "confirm: true")
	})

	t.Run("a confirmed call reaches the backend", func(t *testing.T) {
		seedMCP(t)
		cs := connectMCP(t, newTestMCPServer([]string{allowDestructive}, 200, 256*1024), nil)

		body := errorBodyOf(t, callMCP(t, cs, "iq_data_clear", map[string]any{"source": "snap", "confirm": true}))
		require.Contains(t, body.Error.Message, "does not support clear")
	})

	t.Run("a dry run needs no confirmation", func(t *testing.T) {
		seedMCP(t)
		cs := connectMCP(t, newTestMCPServer([]string{allowDestructive}, 200, 256*1024), nil)

		body := errorBodyOf(t, callMCP(t, cs, "iq_data_drop", map[string]any{"source": "snap", "dry_run": true}))
		require.Contains(t, body.Error.Message, "does not support drop")
	})

	t.Run("delete refuses an empty key list before anything opens", func(t *testing.T) {
		seedMCP(t)
		cs := connectMCP(t, newTestMCPServer([]string{allowDestructive}, 200, 256*1024), nil)

		body := errorBodyOf(t, callMCP(t, cs, "iq_data_delete", map[string]any{"source": "snap", "keys": []string{}}))
		require.Contains(t, body.Error.Message, "at least one key is required")
	})

	t.Run("insert refuses replace without the destructive capability", func(t *testing.T) {
		seedMCP(t)
		cs := connectMCP(t, newTestMCPServer([]string{allowWrites}, 200, 256*1024), nil)

		body := errorBodyOf(t, callMCP(t, cs, "iq_insert", map[string]any{
			"source": "snap", "destination": "snap", "replace": true,
		}))
		require.Contains(t, body.Error.Message, "--allow destructive")
	})
}

// TestMCPDestructiveElicitsConfirmation proves the multi round-trip path: a
// client that can elicit is asked, and the SDK retries the call with the answer.
func TestMCPDestructiveElicitsConfirmation(t *testing.T) {
	tests := []struct {
		name   string
		answer *mcp.ElicitResult
		want   string
	}{
		{
			name:   "accepted, the call proceeds",
			answer: &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}},
			want:   "does not support clear",
		},
		{
			name:   "accepted but answered false, the call is refused",
			answer: &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": false}},
			want:   "declined",
		},
		{
			name:   "declined, the call is refused",
			answer: &mcp.ElicitResult{Action: "decline"},
			want:   "declined",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seedMCP(t)
			var asked string
			var params *mcp.ElicitParams
			cs := connectMCP(t, newTestMCPServer([]string{allowDestructive}, 200, 256*1024), &mcp.ClientOptions{
				ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					asked = req.Params.Message
					params = req.Params
					return tc.answer, nil
				},
			})

			body := errorBodyOf(t, callMCP(t, cs, "iq_data_clear", map[string]any{"source": "snap"}))
			require.Contains(t, asked, "clear snap")
			require.Contains(t, body.Error.Message, tc.want)

			// The request is a form asking one required boolean, and says what
			// the call costs, so a client's user can answer it without context.
			require.NotNil(t, params)
			require.Equal(t, "form", params.Mode)
			require.Contains(t, params.Message, "destroys data")
			require.Equal(t, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"confirm": map[string]any{
						"type":        "boolean",
						"description": "true to proceed, false to abort",
					},
				},
				"required": []any{"confirm"},
			}, params.RequestedSchema)
		})
	}
}

// TestMCPProtocolRevisions runs the same list-and-call against a session on the
// current revision and one negotiated down to the previous one. The SDK exposes
// no client-side version pin, so the older session is produced the way an older
// server produces it: server/discover is unavailable, which sends the client
// through the legacy initialize handshake at 2025-11-25.
func TestMCPProtocolRevisions(t *testing.T) {
	tests := []struct {
		name        string
		noDiscover  bool
		wantVersion string
	}{
		{name: "2026-07-28, server/discover", wantVersion: "2026-07-28"},
		{name: "2025-11-25, initialize", noDiscover: true, wantVersion: "2025-11-25"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seedMCP(t)
			s := newTestMCPServer(nil, 200, 256*1024)
			srv := s.newServer()
			if tc.noDiscover {
				srv.AddReceivingMiddleware(withoutDiscover)
			}
			cs := connectMCPServer(t, srv, nil)
			require.Equal(t, tc.wantVersion, cs.InitializeResult().ProtocolVersion)
			// Tools only on both revisions: the SDK's default set would add the
			// deprecated logging capability, which the legacy handshake still shows.
			require.Equal(t, []string{"tools"}, jsonFieldNames(t, cs.InitializeResult().Capabilities))

			res, err := cs.ListTools(t.Context(), nil)
			require.NoError(t, err)
			require.NotEmpty(t, res.Tools)

			var out mcpQueryOutput
			structOf(t, callMCP(t, cs, "iq_query", map[string]any{"source": "snap", "filter": ".[]"}), &out)
			require.Equal(t, 3, out.Count)
		})
	}
}

// withoutDiscover refuses the stateless discovery RPC, the way a server that
// predates it does, so the client falls back to the legacy handshake.
func withoutDiscover(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method == "server/discover" {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "server/discover is not supported"}
		}
		return next(ctx, method, req)
	}
}

// TestMCPProgressNotifications proves a request carrying a progress token gets
// per-page progress on its own stream, and one without it gets none.
func TestMCPProgressNotifications(t *testing.T) {
	for _, withToken := range []bool{true, false} {
		name := "with a progress token"
		if !withToken {
			name = "without a progress token"
		}
		t.Run(name, func(t *testing.T) {
			seedMCP(t)
			got := make(chan float64, 16)
			var message string
			cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), &mcp.ClientOptions{
				ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
					message = req.Params.Message
					// Routed by the request's own token, so a client can tell
					// which call is reporting.
					if req.Params.ProgressToken != "scan-1" {
						return
					}
					select {
					case got <- req.Params.Progress:
					default:
					}
				},
			})

			params := &mcp.CallToolParams{Name: "iq_query", Arguments: map[string]any{"source": "snap", "filter": ".[]"}}
			if withToken {
				params.SetProgressToken("scan-1")
			}
			res, err := cs.CallTool(t.Context(), params)
			require.NoError(t, err)
			require.False(t, res.IsError, contentText(res))

			if !withToken {
				require.Empty(t, got)
				return
			}
			select {
			case p := <-got:
				require.Positive(t, p)
				require.Equal(t, "scanning", message)
			case <-time.After(5 * time.Second):
				t.Fatal("no progress notification arrived for a request carrying a token")
			}
		})
	}
}

// TestMCPServerIdentity pins what a client learns about the server before its
// first call: the name, the version, the tools-only capability set, and the
// instructions.
func TestMCPServerIdentity(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	res := cs.InitializeResult()
	require.Equal(t, "iq", res.ServerInfo.Name)
	require.Equal(t, "iq", res.ServerInfo.Title)
	require.Equal(t, buildVersion(), res.ServerInfo.Version)
	require.NotNil(t, res.Capabilities.Tools)
	// The tool set is fixed at process start, so the server must not promise a
	// list-changed notification; the SDK's own default would.
	require.False(t, res.Capabilities.Tools.ListChanged)
	// Tools alone: the SDK would otherwise default to advertising logging, which
	// the 2026-07-28 revision deprecates. Asserted over the wire shape so the
	// deprecated fields are never named.
	require.Equal(t, []string{"tools"}, jsonFieldNames(t, res.Capabilities))
	require.Equal(t, mcpInstructions, res.Instructions)
	require.Less(t, strings.Count(mcpInstructions, "\n"), 40, "the instructions must stay short enough to read before a first call")
}

// TestMCPServerLogsThroughTheRunLogger proves the SDK's own activity log is the
// invocation logger, so --log captures the server's session events alongside
// iq's own records rather than losing them to a default sink.
func TestMCPServerLogsThroughTheRunLogger(t *testing.T) {
	seedMCP(t)
	var buf bytes.Buffer
	s := newTestMCPServer(nil, 200, 256*1024)
	s.cfg.logger = slog.New(slog.NewTextHandler(&buf, nil))
	connectMCP(t, s, nil)
	require.Contains(t, buf.String(), "server session connected")
}

// TestListToolsCacheHint drives the middleware directly, including the paths a
// client cannot reach: a result of another type, and a failed call.
func TestListToolsCacheHint(t *testing.T) {
	wantErr := errors.New("boom")
	tests := []struct {
		name     string
		res      mcp.Result
		err      error
		wantTTL  int
		wantCall bool
	}{
		{name: "a tools/list result is stamped", res: &mcp.ListToolsResult{}, wantTTL: mcpListTTLMs, wantCall: true},
		{name: "another result type is untouched", res: &mcp.CallToolResult{}},
		{name: "a failed call is passed through", err: wantErr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := listToolsCacheHint(func(context.Context, string, mcp.Request) (mcp.Result, error) {
				return tc.res, tc.err
			})
			got, err := h(t.Context(), "tools/list", nil)
			require.Equal(t, tc.err, err)
			require.Equal(t, tc.res, got)
			if !tc.wantCall {
				return
			}
			lt, ok := got.(*mcp.ListToolsResult)
			require.True(t, ok)
			require.Equal(t, tc.wantTTL, lt.GetTTLMs())
			require.Equal(t, "private", lt.GetCacheScope())
		})
	}
}

// TestConfirmationAnswer covers the answer decoding directly, including the
// shapes a well-behaved client never sends.
func TestConfirmationAnswer(t *testing.T) {
	withResponses := func(m mcp.InputResponseMap) *mcp.CallToolRequest {
		return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{InputResponses: m}}
	}
	tests := []struct {
		name         string
		req          *mcp.CallToolRequest
		wantApproved bool
		wantAnswered bool
	}{
		{name: "no request"},
		{name: "no params", req: &mcp.CallToolRequest{}},
		{name: "no responses", req: withResponses(nil)},
		{name: "another id", req: withResponses(mcp.InputResponseMap{"other": &mcp.ElicitResult{Action: "accept"}})},
		{
			name:         "accepted and true",
			req:          withResponses(mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}}),
			wantApproved: true, wantAnswered: true,
		},
		{
			name:         "accepted and false",
			req:          withResponses(mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": false}}}),
			wantAnswered: true,
		},
		{
			name:         "accepted with no content",
			req:          withResponses(mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "accept"}}),
			wantAnswered: true,
		},
		{
			name:         "declined",
			req:          withResponses(mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "decline"}}),
			wantAnswered: true,
		},
		{
			name:         "cancelled",
			req:          withResponses(mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "cancel"}}),
			wantAnswered: true,
		},
		{
			name:         "declined with a content that says true",
			req:          withResponses(mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "decline", Content: map[string]any{"confirm": true}}}),
			wantAnswered: true,
		},
		{
			name:         "an answer of another kind",
			req:          withResponses(mcp.InputResponseMap{"confirm": &mcp.ListRootsResult{}}), //nolint:staticcheck // ElicitResult is the only non-deprecated InputResponse; a legacy client can still send this kind.
			wantAnswered: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			approved, answered := confirmationAnswer(tc.req)
			require.Equal(t, tc.wantApproved, approved)
			require.Equal(t, tc.wantAnswered, answered)
		})
	}
}

// TestProgressHooks covers the token check directly: only a request that carries
// one gets callbacks, so a plain call never notifies.
func TestProgressHooks(t *testing.T) {
	s := newTestMCPServer(nil, 200, 256*1024)
	withToken := &mcp.CallToolRequest{Session: &mcp.ServerSession{}, Params: &mcp.CallToolParamsRaw{}}
	withToken.Params.SetProgressToken("t-1")
	noSession := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}
	noSession.Params.SetProgressToken("t-2")
	tests := []struct {
		name     string
		req      *mcp.CallToolRequest
		wantHook bool
	}{
		{name: "no request"},
		{name: "no params", req: &mcp.CallToolRequest{Session: &mcp.ServerSession{}}},
		{name: "no session", req: noSession},
		{name: "no token", req: &mcp.CallToolRequest{Session: &mcp.ServerSession{}, Params: &mcp.CallToolParamsRaw{}}},
		{name: "a token", req: withToken, wantHook: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			onPage, onEstimate := s.progressHooks(t.Context(), tc.req)
			if !tc.wantHook {
				require.Nil(t, onPage)
				require.Nil(t, onEstimate)
				return
			}
			require.NotNil(t, onPage)
			require.NotNil(t, onEstimate)
		})
	}
}

// TestMCPServeStopsOnCancel proves a canceled context ends the server quietly:
// the operator stopping it is not a failure.
func TestMCPServeStopsOnCancel(t *testing.T) {
	seedMCP(t)
	s := newTestMCPServer(nil, 200, 256*1024)
	_, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.NoError(t, s.serve(ctx, st))
}

func TestParseAllow(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    map[string]bool
		wantErr string
	}{
		{name: "no capability", in: nil, want: map[string]bool{}},
		{name: "one capability", in: []string{"writes"}, want: map[string]bool{"writes": true}},
		{
			name: "every capability",
			in:   []string{"writes", "exec", "destructive"},
			want: map[string]bool{"writes": true, "exec": true, "destructive": true},
		},
		{name: "case and space tolerated", in: []string{" Writes "}, want: map[string]bool{"writes": true}},
		{name: "repeated value collapses", in: []string{"exec", "exec"}, want: map[string]bool{"exec": true}},
		{name: "unknown value", in: []string{"admin"}, wantErr: `invalid --allow "admin"`},
		{name: "empty value", in: []string{""}, wantErr: `invalid --allow ""`},
		{name: "a good value does not excuse a bad one", in: []string{"exec", "root"}, wantErr: `invalid --allow "root"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAllow(tc.in)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.Contains(t, err.Error(), "writes, exec, destructive")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestMCPCommandRejectsBadFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown capability", []string{"mcp", "--allow", "root"}, `invalid --allow "root"`},
		{"zero max-items", []string{"mcp", "--max-items", "0"}, "invalid --max-items 0"},
		{"negative max-items", []string{"mcp", "--max-items", "-5"}, "invalid --max-items -5"},
		{"zero max-bytes", []string{"mcp", "--max-bytes", "0"}, "invalid --max-bytes 0"},
		{"negative max-bytes", []string{"mcp", "--max-bytes", "-5"}, "invalid --max-bytes -5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seedMCP(t)
			root, _ := newRootCmd()
			_, err := runCmd(t, root, tc.args...)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestMCPCallTimeoutOnlyTightens(t *testing.T) {
	s := newTestMCPServer(nil, 200, 256*1024)
	s.cfg.timeout = 5 * time.Second
	tests := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr string
	}{
		{name: "unset takes the server's", want: 5 * time.Second},
		{name: "blank takes the server's", in: "  ", want: 5 * time.Second},
		{name: "shorter lowers it", in: "1s", want: time.Second},
		{name: "a nanosecond is the smallest accepted", in: "1ns", want: time.Nanosecond},
		{name: "longer is clamped", in: "1m", want: 5 * time.Second},
		{name: "unparseable", in: "soon", wantErr: "invalid timeout"},
		{name: "zero", in: "0s", wantErr: "invalid timeout"},
		{name: "negative", in: "-1s", wantErr: "invalid timeout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.callTimeout(tc.in)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.Zero(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestCapValue(t *testing.T) {
	tests := []struct {
		name    string
		want    int
		limit   int
		expect  int
		wantErr bool
	}{
		{name: "unset takes the limit", want: 0, limit: 200, expect: 200},
		{name: "lower wins", want: 10, limit: 200, expect: 10},
		{name: "higher is clamped", want: 500, limit: 200, expect: 200},
		{name: "equal passes through", want: 200, limit: 200, expect: 200},
		{name: "negative is refused", want: -1, limit: 200, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := capValue("max_items", tc.want, tc.limit)
			if tc.wantErr {
				require.ErrorContains(t, err, "invalid max_items")
				require.Zero(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expect, got)
		})
	}
}

func TestSampleSize(t *testing.T) {
	zero, all, negative := defaultSchemaSample, 0, -1
	tests := []struct {
		name    string
		in      *int
		want    int
		wantErr bool
	}{
		{name: "unset takes the CLI default", in: nil, want: defaultSchemaSample},
		{name: "explicit zero samples all", in: &all, want: 0},
		{name: "explicit value passes through", in: &zero, want: defaultSchemaSample},
		{name: "negative is refused", in: &negative, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sampleSize(tc.in)
			if tc.wantErr {
				require.ErrorContains(t, err, "invalid sample")
				require.Zero(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBoolOr(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name string
		in   *bool
		def  bool
		want bool
	}{
		{name: "unset takes the default true", in: nil, def: true, want: true},
		{name: "unset takes the default false", in: nil, def: false, want: false},
		{name: "explicit true overrides a false default", in: &yes, def: false, want: true},
		{name: "explicit false overrides a true default", in: &no, def: true, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, boolOr(tc.in, tc.def))
		})
	}
}

func TestToolErrorRendersTheCLIShape(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil stays nil"},
		{
			name: "a plain error becomes the error document",
			err:  errors.New("open source: no such thing"),
			want: `{"error":{"message":"open source: no such thing"}}`,
		},
		{
			name: "a wrapped chain carries its causes",
			err:  fmt.Errorf("open source: %w", errors.New("dial failed")),
			want: `{"error":{"message":"open source: dial failed","causes":["open source","dial failed"]}}`,
		},
		{
			name: "a connection URI is redacted",
			err:  errors.New("dial redis://u:hunter2@h:6379/0 failed"),
			want: `{"error":{"message":"dial redis://u:xxxxx@h:6379/0 failed"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toolError(tc.err)
			if tc.err == nil {
				require.NoError(t, got)
				return
			}
			require.Equal(t, tc.want, got.Error())
		})
	}
}

func TestDecodeJSONObject(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    map[string]any
		wantErr bool
	}{
		{name: "an object decodes", in: `{"a":1}`, want: map[string]any{"a": float64(1)}},
		{name: "an empty object decodes", in: `{}`, want: map[string]any{}},
		{name: "an array is refused", in: `[1]`, wantErr: true},
		{name: "malformed json is refused", in: `{`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeJSONObject([]byte(tc.in))
			if tc.wantErr {
				require.ErrorContains(t, err, "decode introspection")
				var syntaxErr *json.SyntaxError
				var typeErr *json.UnmarshalTypeError
				require.True(t, errors.As(err, &syntaxErr) || errors.As(err, &typeErr), "the JSON error must stay unwrappable: %v", err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestMCPCallConfigCarriesRunSettings proves every run-wide setting the core
// needs reaches a per-call config, on a fresh struct rather than the server's
// own, so concurrent calls cannot race on the source each resolved.
func TestMCPCallConfigCarriesRunSettings(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	s := &mcpServer{cfg: &config{
		timeout:      7 * time.Second,
		decimalMode:  numfmt.DecimalString,
		logger:       logger,
		noCache:      true,
		noCacheIndex: true,
		noCompile:    true,
		handle:       "not carried",
	}}
	got := s.callConfig()
	require.NotSame(t, s.cfg, got)
	require.Equal(t, &config{
		timeout:      7 * time.Second,
		decimalMode:  numfmt.DecimalString,
		logger:       logger,
		noCache:      true,
		noCacheIndex: true,
		noCompile:    true,
	}, got)
}

// TestMCPCommandWiring pins the command's shape: no path completion (its name is
// not a file), no positionals.
func TestMCPCommandWiring(t *testing.T) {
	root, _ := newRootCmd()
	c, _, err := root.Find([]string{"mcp"})
	require.NoError(t, err)
	require.Equal(t, "mcp", c.Name())
	require.NotNil(t, c.ValidArgsFunction, "mcp completion wiring dropped")
	_, dir := c.ValidArgsFunction(c, nil, "")
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
	require.NotNil(t, c.Args, "mcp Args validator dropped")
	require.Error(t, c.Args(c, []string{"extra"}))
}

// TestMCPCommandAcceptsBoundaryCaps proves a cap of exactly one is the smallest
// accepted value: the run passes both cap checks and fails only on the capability
// set, which is validated after them.
func TestMCPCommandAcceptsBoundaryCaps(t *testing.T) {
	seedMCP(t)
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "mcp", "--max-items", "1", "--max-bytes", "1", "--allow", "root")
	require.ErrorContains(t, err, `invalid --allow "root"`)
	require.NotContains(t, err.Error(), "max-")
}

// failingTransport is a transport whose connection attempt fails, the shape of
// a broken stdio pair.
type failingTransport struct{ err error }

func (f failingTransport) Connect(context.Context) (mcp.Connection, error) { return nil, f.err }

// TestMCPServeReportsTransportError proves a transport failure surfaces as a
// wrapped error anchored at iq, while a cancellation mid-run stays quiet.
func TestMCPServeReportsTransportError(t *testing.T) {
	seedMCP(t)
	t.Run("connect failure is wrapped", func(t *testing.T) {
		s := newTestMCPServer(nil, 200, 256*1024)
		boom := errors.New("pipe broken")
		err := s.serve(t.Context(), failingTransport{err: boom})
		require.ErrorIs(t, err, boom)
		require.ErrorContains(t, err, "serve mcp over stdio")
	})
	t.Run("cancel mid-run is not an error", func(t *testing.T) {
		s := newTestMCPServer(nil, 200, 256*1024)
		ct, st := mcp.NewInMemoryTransports()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- s.serve(ctx, st) }()
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(t.Context(), ct, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cs.Close() })
		cancel()
		require.NoError(t, <-done)
	})
}

// TestMCPPingNamesTheSource proves a source named in the call is the one pinged,
// not the active one: the active source is the CLI's default, never the tool's.
func TestMCPPingNamesTheSource(t *testing.T) {
	data := seedMCP(t)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("other", iqfile.URL(data)))
	require.NoError(t, cf.Save())
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	var out mcpPingOutput
	structOf(t, callMCP(t, cs, "iq_ping", map[string]any{"source": "other"}), &out)
	require.Len(t, out.Results, 1)
	require.Equal(t, "other", out.Results[0].Handle)
}

// TestMCPQuerySyntaxErrorNeverConnects proves a filter that does not parse is
// refused before any store is opened: against an unreachable source the error
// is the parse error, not a connection failure.
func TestMCPQuerySyntaxErrorNeverConnects(t *testing.T) {
	seedMCP(t)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("dead", "redis://127.0.0.1:1/0"))
	require.NoError(t, cf.Save())
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	body := errorBodyOf(t, callMCP(t, cs, "iq_query", map[string]any{"source": "dead", "filter": ".["}))
	require.Contains(t, body.Error.Message, "unexpected")
	require.NotContains(t, strings.ToLower(body.Error.Message), "connect")
	require.NotContains(t, strings.ToLower(body.Error.Message), "refused")
}

// TestMCPQueryByteCapIsCumulative proves the byte cap counts the encoded items
// together: a cap sized for exactly the first two items admits two and refuses
// the third.
func TestMCPQueryByteCapIsCumulative(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	var all mcpQueryOutput
	structOf(t, callMCP(t, cs, "iq_query", map[string]any{"source": "snap", "filter": ".[]"}), &all)
	require.Len(t, all.Items, 3)
	first, err := json.Marshal(all.Items[0])
	require.NoError(t, err)
	second, err := json.Marshal(all.Items[1])
	require.NoError(t, err)

	var out mcpQueryOutput
	structOf(t, callMCP(t, cs, "iq_query", map[string]any{
		"source": "snap", "filter": ".[]", "max_bytes": len(first) + len(second),
	}), &out)
	require.True(t, out.Truncated)
	require.Equal(t, 2, out.Count)
	require.Len(t, out.Items, 2)

	var short mcpQueryOutput
	structOf(t, callMCP(t, cs, "iq_query", map[string]any{
		"source": "snap", "filter": ".[]", "max_bytes": len(first) + len(second) - 1,
	}), &short)
	require.True(t, short.Truncated)
	require.Equal(t, 1, short.Count)
}

// TestMCPProgressAccumulatesAcrossPages proves the progress a client sees is the
// running total of items scanned, not the size of the latest page: a keyspace
// larger than one page ends at its full count.
func TestMCPProgressAccumulatesAcrossPages(t *testing.T) {
	const rows = 1200 // the file driver pages 500 at a time
	seedMCP(t)
	dir := t.TempDir()
	var b strings.Builder
	for i := range rows {
		fmt.Fprintf(&b, `{"key":"k%04d","value":{"n":%d}}`+"\n", i, i)
	}
	data := filepath.Join(dir, "big.jsonl")
	require.NoError(t, os.WriteFile(data, []byte(b.String()), 0o600))
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("big", iqfile.URL(data)))
	require.NoError(t, cf.Save())

	got := make(chan float64, 64)
	cs := connectMCP(t, newTestMCPServer(nil, rows, 1<<20), &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			got <- req.Params.Progress
		},
	})
	params := &mcp.CallToolParams{Name: "iq_query", Arguments: map[string]any{"source": "big", "filter": ".[] | .n"}}
	params.SetProgressToken("scan-big")
	res, err := cs.CallTool(t.Context(), params)
	require.NoError(t, err)
	require.False(t, res.IsError, contentText(res))

	var progress []float64
	deadline := time.After(5 * time.Second)
	for len(progress) == 0 || progress[len(progress)-1] < rows {
		select {
		case p := <-got:
			progress = append(progress, p)
		case <-deadline:
			t.Fatalf("progress never reached %d: %v", rows, progress)
		}
	}
	require.Greater(t, len(progress), 1, "a multi-page scan must report more than once")
	require.IsIncreasing(t, progress)
	require.InDelta(t, float64(rows), progress[len(progress)-1], 0)

	// The item cap ends the scan on the page that fills it: no later page is
	// read, so no progress report ever nears the keyspace size.
	capped := make(chan float64, 64)
	cs2 := connectMCP(t, newTestMCPServer(nil, 10, 1<<20), &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			capped <- req.Params.Progress
		},
	})
	params = &mcp.CallToolParams{Name: "iq_query", Arguments: map[string]any{"source": "big", "filter": ".[] | .n"}}
	params.SetProgressToken("scan-capped")
	res, err = cs2.CallTool(t.Context(), params)
	require.NoError(t, err)
	var out mcpQueryOutput
	structOf(t, res, &out)
	require.True(t, out.Truncated)
	require.Equal(t, 10, out.Count)
	time.Sleep(200 * time.Millisecond) // let any straggling notification land
	for {
		select {
		case p := <-capped:
			require.Less(t, p, float64(rows), "the scan must stop once the cap is reached")
		default:
			return
		}
	}
}

// TestMCPQueryNoMatchReturnsEmptyList proves a query matching nothing returns an
// empty items array, never null: the output schema promises an array, and a
// client iterating the result must not have to special-case the empty case.
func TestMCPQueryNoMatchReturnsEmptyList(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	res := callMCP(t, cs, "iq_query", map[string]any{"source": "snap", "filter": `.[] | select(.status == "nope")`})
	var out mcpQueryOutput
	structOf(t, res, &out)
	require.Equal(t, 0, out.Count)
	require.NotNil(t, out.Items, "items must serialize as [] rather than null")
	require.Contains(t, contentText(res), `"items":[]`)
}

// TestMCPExplainBoundedReadNamesKeys proves a key-addressed filter explains as a
// bounded read naming its keys, so the classification a client reads matches
// the route the query takes.
func TestMCPExplainBoundedReadNamesKeys(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	var out mcpExplainOutput
	structOf(t, callMCP(t, cs, "iq_explain", map[string]any{"source": "snap", "filter": `.["alpha"]`}), &out)
	require.Equal(t, []string{"alpha"}, out.Classification.Keys)
	require.False(t, out.Classification.Scan)
}

// TestMCPDiffStatsWithoutFilter proves the stats layer runs when no filter is
// given: the refusal is for a filter that has nothing to scope, not for the
// layer itself.
func TestMCPDiffStatsWithoutFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable MongoDB")
	}
	u := os.Getenv("IQ_MONGO_URL")
	if u == "" {
		u = "mongodb://localhost:27017/iq"
	}
	seedMongo(t, u, "mcp_stats_a", []string{`{"_id":"a","n":1}`})
	seedMongo(t, u, "mcp_stats_b", nil)
	seedMCP(t)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("live", u))
	require.NoError(t, cf.Save())
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	t.Run("same server, same build", func(t *testing.T) {
		var out mcpDiffOutput
		structOf(t, callMCP(t, cs, "iq_diff", map[string]any{
			"a": "live.mcp_stats_a", "b": "live.mcp_stats_b", "stats": true, "section": []string{"buildInfo"},
		}), &out)
		require.False(t, out.Differ)
		require.Empty(t, out.Stats)
	})

	t.Run("different collections differ on stats alone", func(t *testing.T) {
		var out mcpDiffOutput
		structOf(t, callMCP(t, cs, "iq_diff", map[string]any{
			"a": "live.mcp_stats_a", "b": "live.mcp_stats_b", "stats": true, "section": []string{"collStats"},
		}), &out)
		require.True(t, out.Differ)
		require.NotEmpty(t, out.Stats)
		require.Empty(t, out.Data, "the data layer must not run for a stats-only diff")
		require.Empty(t, out.Schema)
	})
}

// TestMCPPingReportsFailure proves an unreachable source is a row with ok false
// and a redacted reason, not a tool error, so one dead member never hides the
// live ones.
func TestMCPPingReportsFailure(t *testing.T) {
	seedMCP(t)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("dead", "redis://u:hunter2@127.0.0.1:1/0"))
	require.NoError(t, cf.Save())
	s := newTestMCPServer(nil, 200, 256*1024)
	s.cfg.timeout = 2 * time.Second
	cs := connectMCP(t, s, nil)

	var out mcpPingOutput
	structOf(t, callMCP(t, cs, "iq_ping", map[string]any{"source": "dead"}), &out)
	require.Len(t, out.Results, 1)
	require.False(t, out.Results[0].OK)
	require.NotEmpty(t, out.Results[0].Error)
	require.NotContains(t, out.Results[0].Error, "hunter2")
	require.Zero(t, out.Results[0].ElapsedMs)
}

// TestMCPPingLiveRedis proves a reachable source reports the round trip it
// measured: a fraction of a millisecond on localhost, never a rounded zero,
// and never the whole timeout.
func TestMCPPingLiveRedis(t *testing.T) {
	u := liveRedisURL(t)
	seedMCP(t)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("live", u))
	require.NoError(t, cf.Save())
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	var out mcpPingOutput
	structOf(t, callMCP(t, cs, "iq_ping", map[string]any{"source": "live"}), &out)
	require.Len(t, out.Results, 1)
	require.True(t, out.Results[0].OK, out.Results[0].Error)
	require.Equal(t, "redis", out.Results[0].Driver)
	require.Greater(t, out.Results[0].ElapsedMs, 0.001)
	require.Less(t, out.Results[0].ElapsedMs, 1000.0)
}

// TestMCPInspectRejectsUnknownSource proves inspect fails at source resolution,
// before any store is opened.
func TestMCPInspectRejectsUnknownSource(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	body := errorBodyOf(t, callMCP(t, cs, "iq_inspect", map[string]any{"source": "nope"}))
	require.Contains(t, body.Error.Message, `unknown source "nope"`)
}

// TestMCPInsertDryRunReplaceNeedsNoConfirmation proves a dry run of a replace
// asks nothing: it writes and destroys nothing, so there is nothing to confirm.
func TestMCPInsertDryRunReplaceNeedsNoConfirmation(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer([]string{allowWrites, allowDestructive}, 200, 256*1024), nil)

	res := callMCP(t, cs, "iq_insert", map[string]any{
		"source": "snap", "destination": "snap", "replace": true, "dry_run": true,
	})
	require.NotContains(t, contentText(res), "confirmation")
	require.Empty(t, res.InputRequests)
}

// TestMCPInsertWithoutReplaceNeedsOnlyWrites proves the destructive capability
// gates replace alone: a plain insert under --allow writes is never refused
// for it.
func TestMCPInsertWithoutReplaceNeedsOnlyWrites(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer([]string{allowWrites}, 200, 256*1024), nil)

	res := callMCP(t, cs, "iq_insert", map[string]any{"source": "snap", "destination": "snap", "dry_run": true})
	require.NotContains(t, contentText(res), "destructive capability")
}

// liveRedisURL returns the Redis the integration suite provisions, or skips.
func liveRedisURL(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration: needs a reachable Redis")
	}
	if u := os.Getenv("IQ_REDIS_URL"); u != "" {
		return u
	}
	return "redis://localhost:6379/0"
}

// TestMCPInspectLiveRedis drives iq_inspect against a reachable Redis, whose
// client refuses a nil context: the call runs under the per-call deadline, in
// the open and in every introspection command.
func TestMCPInspectLiveRedis(t *testing.T) {
	u := liveRedisURL(t)
	seedMCP(t)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("live", u))
	require.NoError(t, cf.Save())
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	res := callMCP(t, cs, "iq_inspect", map[string]any{"source": "live", "only": []string{"server"}})
	require.False(t, res.IsError, contentText(res))
	sections, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok)
	server, ok := sections["Server"].(map[string]any)
	require.True(t, ok, "Server section: %v", sections)
	require.NotEmpty(t, server["redis_version"])
}

// TestMCPDiffStatsLiveRedis proves the stats layer runs its introspection under
// the per-call deadline against a client that refuses a nil context.
func TestMCPDiffStatsLiveRedis(t *testing.T) {
	u := liveRedisURL(t)
	seedMCP(t)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("live", u))
	require.NoError(t, cf.Save())
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	// The server section carries uptime, so even one server differs from
	// itself; the assertion is that the layer ran.
	var out mcpDiffOutput
	structOf(t, callMCP(t, cs, "iq_diff", map[string]any{
		"a": "live", "b": "live", "stats": true, "section": []string{"server"},
	}), &out)
	require.Empty(t, out.Data)
}

// TestMCPInsertFromLiveRedis proves a write runs under the per-call deadline:
// the source scan and the destination open both go through a client that
// refuses a nil context.
func TestMCPInsertFromLiveRedis(t *testing.T) {
	u := liveRedisURL(t)
	seedRedis(t, u, map[string]string{"k": "v"})
	seedMCP(t)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("live", u))
	require.NoError(t, cf.Save())
	cs := connectMCP(t, newTestMCPServer([]string{allowWrites}, 200, 256*1024), nil)

	var out mcpWriteOutput
	structOf(t, callMCP(t, cs, "iq_insert", map[string]any{
		"source": "live", "destination": "live", "dry_run": true, "key_prefix": "copy:",
	}), &out)
	require.True(t, out.DryRun)
	require.Equal(t, 1, out.Written)
	require.Equal(t, "live", out.Destination)

	// A real plain insert asks no confirmation: only replace destroys data.
	// It lands in a second, emptied database so the scan never sees its own
	// writes and every counter below is exact.
	u1 := strings.TrimSuffix(u, "/0") + "/1"
	seedRedis(t, u1, nil)
	require.NoError(t, cf.Add("live1", u1))
	require.NoError(t, cf.Save())
	res := callMCP(t, cs, "iq_insert", map[string]any{"source": "live", "destination": "live1"})
	require.Empty(t, res.InputRequests)
	require.False(t, res.IsError, contentText(res))
	structOf(t, res, &out)
	require.False(t, out.DryRun)
	require.Equal(t, mcpWriteOutput{Destination: "live1", Written: 1}, out)

	// The same insert again: no_overwrite defaults to true, so the existing key
	// is skipped and reported as such.
	res = callMCP(t, cs, "iq_insert", map[string]any{"source": "live", "destination": "live1"})
	require.False(t, res.IsError, contentText(res))
	structOf(t, res, &out)
	require.Equal(t, mcpWriteOutput{Destination: "live1", Skipped: 1}, out)

	// With no_overwrite off the existing key is overwritten and reported as such.
	res = callMCP(t, cs, "iq_insert", map[string]any{"source": "live", "destination": "live1", "no_overwrite": false})
	require.False(t, res.IsError, contentText(res))
	structOf(t, res, &out)
	require.Equal(t, mcpWriteOutput{Destination: "live1", Overwritten: 1}, out)

	// Under --allow destructive a dry run of a replace is accepted outright:
	// the capability gate lets it through and a dry run has nothing to confirm.
	// The file-backed source cannot show this, a file destination is refused
	// before the write, so only a live store proves the gate opens.
	cs = connectMCP(t, newTestMCPServer([]string{allowWrites, allowDestructive}, 200, 256*1024), nil)
	res = callMCP(t, cs, "iq_insert", map[string]any{
		"source": "live", "destination": "live", "replace": true, "dry_run": true,
	})
	require.False(t, res.IsError, contentText(res))
	require.Empty(t, res.InputRequests)
	structOf(t, res, &out)
	require.True(t, out.DryRun)
}

// TestConfirmationWithoutClientCapabilities proves a request from a session that
// declared no capabilities at all is refused with the confirm-hint, not asked:
// there is no elicitation to fall back on, and no capability set to read.
func TestConfirmationWithoutClientCapabilities(t *testing.T) {
	s := newTestMCPServer([]string{allowDestructive}, 200, 256*1024)
	req := &mcp.CallToolRequest{Session: &mcp.ServerSession{}, Params: &mcp.CallToolParamsRaw{}}
	require.Nil(t, req.ClientCapabilities())

	res, ok := s.confirmation(req, false, "drop snap")
	require.False(t, ok)
	require.NotNil(t, res)
	require.True(t, res.IsError)
	require.Contains(t, contentText(res), "confirm: true")
}

// TestMCPExplainHonoursUnbounded proves the call's unbounded flag reaches the
// plan: the same streamable filter is marked as materialized once the caller
// accepts loading the keyspace.
func TestMCPExplainHonoursUnbounded(t *testing.T) {
	seedMCP(t)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	var streamed mcpExplainOutput
	structOf(t, callMCP(t, cs, "iq_explain", map[string]any{"source": "snap", "filter": ".[]"}), &streamed)
	require.Contains(t, streamed.Plan, "streaming scan")

	var materialized mcpExplainOutput
	structOf(t, callMCP(t, cs, "iq_explain", map[string]any{"source": "snap", "filter": ".[]", "unbounded": true}), &materialized)
	require.Contains(t, materialized.Plan, "materialized scan")
	require.NotContains(t, materialized.Plan, "streaming scan")
}

// TestInsertTransformOptions pins the tool-to-core mapping of the reshaping
// inputs, field by field, so a dropped option cannot pass unseen.
func TestInsertTransformOptions(t *testing.T) {
	got := insertTransformOptions(mcpInsertInput{
		Filter: "{id}", Key: ".id", KeyField: "id", KeyPrefix: "p:", Type: "hash",
	})
	require.Equal(t, query.TransformOptions{
		Filter: "{id}", Key: ".id", KeyField: "id", KeyPrefix: "p:", Type: "hash",
	}, got)
}

// TestInsertRequestFrom pins how the tool inputs become the shared write
// request: insert-only unless no_overwrite is turned off, replace and dry_run
// pass through, and the confirmation never prompts.
func TestInsertRequestFrom(t *testing.T) {
	no := false
	yes := true
	tests := []struct {
		name string
		in   mcpInsertInput
		want insertRequest
	}{
		{name: "defaults to insert-only", in: mcpInsertInput{Destination: "cache"}, want: insertRequest{dst: "cache", mode: query.InsertOnly}},
		{name: "no_overwrite true is insert-only", in: mcpInsertInput{Destination: "cache", NoOverwrite: &yes}, want: insertRequest{dst: "cache", mode: query.InsertOnly}},
		{name: "no_overwrite false is upsert", in: mcpInsertInput{Destination: "cache", NoOverwrite: &no}, want: insertRequest{dst: "cache", mode: query.Upsert}},
		{name: "replace and dry_run pass through", in: mcpInsertInput{Destination: "cache", Replace: true, DryRun: true}, want: insertRequest{dst: "cache", mode: query.InsertOnly, replace: true, dryRun: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := insertRequestFrom(tc.in)
			require.NotNil(t, got.confirm)
			require.NoError(t, got.confirm("empty cache before writing"))
			got.confirm = nil
			require.Equal(t, tc.want, got)
		})
	}
}

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/diff"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/shape"
)

// errTruncated stops a run once a result cap is reached. The core returns the
// moment an emit callback errors and every driver's scan stops on the first
// callback error, so this is how a consumer-side cap ends a scan mid-page. It is
// a control signal, never surfaced to a client.
var errTruncated = errors.New("result cap reached")

// mcpToolError carries an already-rendered iq error as the JSON body a client
// sees. The SDK sets a tool result's single text content to err.Error() and marks
// it isError, so making Error() the redacted {"error":{...}} document is how the
// MCP surface reuses the CLI's --error.format json shape exactly.
type mcpToolError struct{ body string }

// Error returns the rendered error document.
func (e *mcpToolError) Error() string { return e.body }

// toolError renders err into the CLI's JSON error shape, redacted, for return
// from a tool handler. A nil error yields nil, so a handler can return it
// unconditionally.
func toolError(err error) error {
	if err == nil {
		return nil
	}
	var b bytes.Buffer
	renderErrorJSON(&b, err)
	return &mcpToolError{body: strings.TrimRight(b.String(), "\n")}
}

// addTools registers every tool the server's --allow set permits, in sorted name
// order so tools/list is deterministic. A tool that is not allowed is never
// registered, so it is absent from tools/list and cannot be called: the
// annotations below are display hints, and --allow is the only gate.
func (s *mcpServer) addTools(srv *mcp.Server) {
	// The title is set twice on purpose: Tool.Title is the current field, and
	// Annotations.Title is the only one a client on a revision older than
	// 2025-06-18 reads, which the SDK still negotiates down to.
	readOnly := func(title string) *mcp.ToolAnnotations {
		return &mcp.ToolAnnotations{
			Title: title, ReadOnlyHint: true, IdempotentHint: true,
			DestructiveHint: new(false), OpenWorldHint: new(false),
		}
	}
	destructive := func(title string, idempotent bool) *mcp.ToolAnnotations {
		return &mcp.ToolAnnotations{
			Title: title, ReadOnlyHint: false, IdempotentHint: idempotent,
			DestructiveHint: new(true), OpenWorldHint: new(false),
		}
	}

	if s.allows(allowDestructive) {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "iq_data_clear",
			Title:       "Empty a container",
			Description: "Empty a source's container, keeping it (MongoDB deleteMany({}), Redis FLUSHDB). Destroys the stored data. Pass dry_run to report the effect without changing anything; a real run needs confirm.",
			Annotations: destructive("Empty a container", true),
		}, s.toolDataClear)
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "iq_data_delete",
			Title:       "Delete specific keys",
			Description: "Remove named keys from a source's container, keeping the container. A key already absent is not an error. Pass dry_run to report the effect without changing anything; a real run needs confirm.",
			Annotations: destructive("Delete specific keys", true),
		}, s.toolDataDelete)
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "iq_data_drop",
			Title:       "Drop a container",
			Description: "Remove a source's container entirely (MongoDB drops the collection and its indexes). Rejected on a backend with no droppable container, such as Redis. Pass dry_run to report the effect without changing anything; a real run needs confirm.",
			Annotations: destructive("Drop a container", true),
		}, s.toolDataDrop)
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "iq_diff",
		Title:       "Compare two sources",
		Description: "Compare two saved sources: data (item by item, keyed), stats (native introspection, same driver only), or schema (an inferred field/type shape, cross-driver). Selecting no layer diffs the data.",
		Annotations: readOnly("Compare two sources"),
	}, s.toolDiff)
	if s.allows(allowExec) {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "iq_exec",
			Title:       "Run a native backend command",
			Description: "Forward a command to the backend verbatim, in the backend's own language: a Redis command and operands, a MongoDB runCommand document, CQL, PartiQL, a Mango request, SQL++, Cypher, an Elasticsearch search body, or an HBase verb. It can write, so it is behind --allow exec.",
			Annotations: destructive("Run a native backend command", false),
		}, s.toolExec)
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "iq_explain",
		Title:       "Explain a query plan",
		Description: "Compute the access plan for a filter without connecting: the route (bounded read, streaming scan, materialized scan), the backend calls, and which select() conjuncts the backend evaluates. Run it before any scan.",
		Annotations: readOnly("Explain a query plan"),
	}, s.toolExplain)
	if s.allows(allowWrites) {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "iq_insert",
			Title:       "Copy items into another source",
			Description: "Copy a source's items into a destination source, native types intact, optionally transformed by a jq filter. no_overwrite defaults to true, so existing keys are skipped. replace empties the destination first and needs --allow destructive and a confirmation. Preview with dry_run.",
			Annotations: &mcp.ToolAnnotations{
				Title: "Copy items into another source", ReadOnlyHint: false, IdempotentHint: true,
				DestructiveHint: new(true), OpenWorldHint: new(false),
			},
		}, s.toolInsert)
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "iq_inspect",
		Title:       "Inspect a source",
		Description: "Show a source's native server or database introspection (MongoDB diagnostic commands, Redis INFO, Cassandra system tables, and their kin). Use only to narrow to named sections.",
		Annotations: readOnly("Inspect a source"),
	}, s.toolInspect)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "iq_ping",
		Title:       "Check a source is reachable",
		Description: "Open a source and round-trip one cheap backend command, reporting the driver and the round-trip time, or the error. With no source it pings the active one; a group name pings every member.",
		Annotations: readOnly("Check a source is reachable"),
	}, s.toolPing)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "iq_query",
		Title:       "Query a source with jq",
		Description: "Run a jq filter against a source. The filter is both the key selector and the transform: '.[\"k\"]' fetches one key, '.[] | select(...)' streams the keyspace. A filter that needs the whole keyspace at once is refused unless unbounded is set. The result is capped by max_items and max_bytes.",
		Annotations: readOnly("Query a source with jq"),
	}, s.toolQuery)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "iq_schema",
		Title:       "Infer a source's schema",
		Description: "Sample a source and return a JSON Schema draft 2020-12 inferred from its values. The shape is sampled, never declared, so a wider sample yields a truer shape. A few hundred bytes, not a dump of documents.",
		Annotations: readOnly("Infer a source's schema"),
	}, s.toolSchema)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "iq_sources",
		Title:       "List the saved sources",
		Description: "List the sources this server may reach: the handle to address, the driver, and the connection URI with any password redacted. Start here; never guess a handle.",
		Annotations: readOnly("List the saved sources"),
	}, s.toolSources)
}

// mcpSourcesInput narrows the listing to one group.
type mcpSourcesInput struct {
	Group string `json:"group,omitempty" jsonschema:"list only the sources in this group"`
}

// mcpSourcesOutput is the registry listing, one row per source.
type mcpSourcesOutput struct {
	Sources []sourceRow `json:"sources"`
	Active  string      `json:"active,omitempty"`
}

// toolSources lists the saved sources through the same projection `iq ls --json`
// uses, so a location is redacted here exactly as it is there.
func (s *mcpServer) toolSources(_ context.Context, _ *mcp.CallToolRequest, in mcpSourcesInput) (*mcp.CallToolResult, mcpSourcesOutput, error) {
	cf, err := iqconfig.Load()
	if err != nil {
		return nil, mcpSourcesOutput{}, toolError(err)
	}
	list := cf.List()
	if in.Group != "" {
		kept := list[:0:0]
		for _, h := range list {
			if h.Name == in.Group || strings.HasPrefix(h.Name, in.Group+"/") {
				kept = append(kept, h)
			}
		}
		list = kept
	}
	return nil, mcpSourcesOutput{Sources: sourceRows(cf, list, true, false, false), Active: cf.Active}, nil
}

// mcpPingInput names the source, group, or nothing (the active source) to check.
type mcpPingInput struct {
	Source string `json:"source,omitempty" jsonschema:"the source handle or group to check; omit for the active source"`
}

// mcpPingResult is one source's reachability outcome.
type mcpPingResult struct {
	Handle    string  `json:"handle"`
	Driver    string  `json:"driver"`
	OK        bool    `json:"ok"`
	ElapsedMs float64 `json:"elapsed_ms"`
	Error     string  `json:"error,omitempty"`
}

// mcpPingOutput carries one result per checked source.
type mcpPingOutput struct {
	Results []mcpPingResult `json:"results"`
}

// toolPing checks reachability through the same round-trip `iq ping` performs.
// A per-source failure is reported in the row, not as a tool error, so one
// unreachable member of a group does not hide the reachable ones.
func (s *mcpServer) toolPing(ctx context.Context, _ *mcp.CallToolRequest, in mcpPingInput) (*mcp.CallToolResult, mcpPingOutput, error) {
	cf, err := iqconfig.Load()
	if err != nil {
		return nil, mcpPingOutput{}, toolError(err)
	}
	var args []string
	if in.Source != "" {
		args = []string{in.Source}
	}
	targets, err := pingTargets(cf, args, false)
	if err != nil {
		return nil, mcpPingOutput{}, toolError(err)
	}
	// pingTargets returns at least one target or an error, so the appends below
	// always leave a non-nil array for the output schema.
	var out mcpPingOutput
	for _, t := range targets {
		row := mcpPingResult{Handle: t.handle, Driver: driverName(t.source.URL)}
		if d, perr := pingOne(ctx, t, s.cfg.timeout); perr != nil {
			row.Error = redactMessage(oneLine(perr))
		} else {
			row.OK = true
			row.ElapsedMs = float64(d) / float64(time.Millisecond)
		}
		out.Results = append(out.Results, row)
	}
	return nil, out, nil
}

// mcpExplainInput is the plan request: the source to plan against and the filter
// to plan, under the same pushdown and unbounded switches the query takes.
type mcpExplainInput struct {
	Source    string `json:"source,omitempty" jsonschema:"the source handle to plan against, optionally handle.keyspace; omit only for a filter that reads entirely through source()"`
	Filter    string `json:"filter" jsonschema:"the jq filter to plan"`
	Compile   *bool  `json:"compile,omitempty" jsonschema:"push select() conjuncts to the backend where it supports them; true by default"`
	Unbounded bool   `json:"unbounded,omitempty" jsonschema:"plan the filter as if it may materialize the whole keyspace"`
}

// mcpClassification is the selector's verdict on a filter: the keys it names,
// whether it distributes over the keyspace, and whether it needs a scan.
type mcpClassification struct {
	Keys       []string `json:"keys"`
	Streamable bool     `json:"streamable"`
	Scan       bool     `json:"scan"`
}

// mcpExplainOutput is the access plan: the same facts the structured "query plan"
// log record carries (sourcePlan.logAttrs), plus the rendered plan text.
type mcpExplainOutput struct {
	Handle         string            `json:"handle"`
	Driver         string            `json:"driver"`
	Classification mcpClassification `json:"classification"`
	Ops            []string          `json:"ops"`
	Filter         map[string]any    `json:"filter,omitempty"`
	Conjuncts      []conjunctPlan    `json:"conjuncts,omitempty"`
	Plan           string            `json:"plan"`
}

// toolExplain computes the plan without connecting. A filter that reads through
// source() has no single backend, so it returns the plan text alone.
func (s *mcpServer) toolExplain(_ context.Context, _ *mcp.CallToolRequest, in mcpExplainInput) (*mcp.CallToolResult, mcpExplainOutput, error) {
	if strings.TrimSpace(in.Filter) == "" {
		return nil, mcpExplainOutput{}, toolError(errors.New("explain: a filter is required"))
	}
	cross, err := query.UsesSource(in.Filter)
	if err != nil {
		return nil, mcpExplainOutput{}, toolError(asSyntaxError(in.Filter, err))
	}
	cfg := s.callConfig()
	cfg.src = in.Source
	cfg.unbounded = in.Unbounded
	cfg.noCompile = !boolOr(in.Compile, true)
	if !cross {
		if err := resolveSource(cfg); err != nil {
			return nil, mcpExplainOutput{}, toolError(err)
		}
	}
	text, err := buildJQPlan(cfg, in.Filter, cross, true)
	if err != nil {
		return nil, mcpExplainOutput{}, toolError(err)
	}
	// keys and ops are always-present arrays in the result schema, so they start
	// empty rather than nil: a nil slice would serialize as null and fail the
	// output schema for a cross-source filter or a driver with no describer. A
	// cross-source filter has no URI, so the source plan below has no driver and
	// leaves every per-source field at that empty value.
	out := mcpExplainOutput{Plan: text, Ops: []string{}, Classification: mcpClassification{Keys: []string{}}}
	sp, err := buildSourcePlan(cfg.url, in.Filter, !cfg.noCompile, cfg.unbounded)
	if err != nil {
		return nil, mcpExplainOutput{}, toolError(err)
	}
	out.Handle = cfg.handle
	if sp.hasPlan {
		out.Driver = sp.driver
		out.Classification.Streamable = sp.keys.Streamable
		out.Classification.Scan = sp.keys.Scan
		if sp.keys.Keys != nil {
			out.Classification.Keys = sp.keys.Keys
		}
		if sp.ops != nil {
			out.Ops = sp.ops
		}
		out.Filter = sp.filter
		out.Conjuncts = sp.conjuncts
	}
	return nil, out, nil
}

// mcpQueryInput is a query request: the source, the filter, and the per-call
// bounds, each of which may only tighten the server's own caps.
type mcpQueryInput struct {
	Source    string `json:"source,omitempty" jsonschema:"the source handle to query, optionally handle.keyspace; omit only for a filter that reads entirely through source()"`
	Filter    string `json:"filter" jsonschema:"the jq filter to run; single top-level paths name the keys to fetch"`
	Unbounded bool   `json:"unbounded,omitempty" jsonschema:"permit a filter that loads the whole keyspace into memory; ask the user first"`
	Compile   *bool  `json:"compile,omitempty" jsonschema:"push select() conjuncts to the backend where it supports them; true by default, results are unchanged either way"`
	MaxItems  int    `json:"max_items,omitempty" jsonschema:"cap the returned items; may only lower the server's own cap"`
	MaxBytes  int    `json:"max_bytes,omitempty" jsonschema:"cap the encoded size of the returned items in bytes; may only lower the server's own cap"`
	Timeout   string `json:"timeout,omitempty" jsonschema:"bound the call, a Go duration such as 10s; may only lower the server's own --timeout"`
}

// mcpQueryOutput is the produced values, their count, and whether a cap cut the
// run short.
type mcpQueryOutput struct {
	Items     []any `json:"items"`
	Count     int   `json:"count"`
	Truncated bool  `json:"truncated"`
}

// toolQuery runs the filter through the same engine the CLI query uses, with an
// emit closure that collects values until a cap is reached. Reaching a cap is a
// successful, truncated result, not an error; a filter that needs the whole
// keyspace without unbounded surfaces the core's refusal as an error result.
func (s *mcpServer) toolQuery(ctx context.Context, req *mcp.CallToolRequest, in mcpQueryInput) (*mcp.CallToolResult, mcpQueryOutput, error) {
	if strings.TrimSpace(in.Filter) == "" {
		return nil, mcpQueryOutput{}, toolError(errors.New("query: a filter is required"))
	}
	cross, err := query.UsesSource(in.Filter)
	if err != nil {
		return nil, mcpQueryOutput{}, toolError(asSyntaxError(in.Filter, err))
	}
	maxItems, err := capValue("max_items", in.MaxItems, s.maxItems)
	if err != nil {
		return nil, mcpQueryOutput{}, toolError(err)
	}
	maxBytes, err := capValue("max_bytes", in.MaxBytes, s.maxBytes)
	if err != nil {
		return nil, mcpQueryOutput{}, toolError(err)
	}
	timeout, err := s.callTimeout(in.Timeout)
	if err != nil {
		return nil, mcpQueryOutput{}, toolError(err)
	}

	cfg := s.callConfig()
	cfg.src = in.Source
	cfg.unbounded = in.Unbounded
	cfg.noCompile = !boolOr(in.Compile, true)
	if !cross {
		if err := resolveSource(cfg); err != nil {
			return nil, mcpQueryOutput{}, toolError(err)
		}
	}

	out := mcpQueryOutput{Items: []any{}}
	size := 0
	emit := func(v any) error {
		if len(out.Items) >= maxItems {
			out.Truncated = true
			return errTruncated
		}
		enc, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("encode result item: %w", err)
		}
		if size+len(enc) > maxBytes {
			out.Truncated = true
			return errTruncated
		}
		size += len(enc)
		out.Items = append(out.Items, v)
		return nil
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	opts := query.RunOptions{Unbounded: cfg.unbounded, Compile: !cfg.noCompile, Logger: cfg.log()}
	opts.OnPage, opts.OnEstimate = s.progressHooks(cctx, req)

	var runErr error
	if cross {
		cf, err := iqconfig.Load()
		if err != nil {
			return nil, mcpQueryOutput{}, toolError(err)
		}
		opener := newSourceOpener(cf, nil, cfg.decimalMode, cfg.noCache, cfg.noCacheIndex)
		defer opener.closeAll()
		runErr = query.NewCrossEngine(opener).Run(cctx, in.Filter, opts, emit)
	} else {
		st, err := openStore(cctx, cfg)
		if err != nil {
			return nil, mcpQueryOutput{}, toolError(redactErr(err, cfg.url))
		}
		defer func() { _ = st.Close() }()
		runErr = query.NewJQEngine(st).Run(cctx, in.Filter, opts, emit)
	}
	if runErr != nil && !errors.Is(runErr, errTruncated) {
		// No store echoes its URI into a run error, and the rendered error is
		// redacted by shape anyway, so the chain goes through intact: a parse
		// error keeps its position and a scan refusal keeps its hint.
		return nil, mcpQueryOutput{}, toolError(scanHint(asSyntaxError(in.Filter, runErr)))
	}
	out.Count = len(out.Items)
	return nil, out, nil
}

// mcpInspectInput names the source and, optionally, the sections to keep.
type mcpInspectInput struct {
	Source string   `json:"source,omitempty" jsonschema:"the source handle to inspect, optionally handle.keyspace; omit for the active source"`
	Only   []string `json:"only,omitempty" jsonschema:"narrow to these introspection sections or subcommands; omit for the source's full set"`
}

// toolInspect runs the driver's own introspection through the same dispatch
// `iq inspect` uses and returns its machine-readable rendering.
func (s *mcpServer) toolInspect(ctx context.Context, _ *mcp.CallToolRequest, in mcpInspectInput) (*mcp.CallToolResult, map[string]any, error) {
	cfg := s.callConfig()
	if err := resolveInspectSource(cfg, in.Source); err != nil {
		return nil, nil, toolError(err)
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.timeout)
	defer cancel()
	st, err := openStore(cctx, cfg)
	if err != nil {
		return nil, nil, toolError(redactErr(err, cfg.url))
	}
	defer func() { _ = st.Close() }()
	var b bytes.Buffer
	if err := dispatchInspect(cctx, &b, st, cfg, in.Only, true, false, false); err != nil {
		return nil, nil, toolError(redactErr(err, cfg.url))
	}
	out, err := decodeJSONObject(b.Bytes())
	if err != nil {
		return nil, nil, toolError(err)
	}
	return nil, out, nil
}

// mcpSchemaInput names the source, how wide to sample it, and an optional filter
// scoping which items the shape is inferred from.
type mcpSchemaInput struct {
	Source string `json:"source,omitempty" jsonschema:"the source handle to sample, optionally handle.keyspace; omit for the active source"`
	Filter string `json:"filter,omitempty" jsonschema:"a jq filter scoping which items the shape is inferred from"`
	Sample *int   `json:"sample,omitempty" jsonschema:"max items sampled; omit for 1000, 0 samples all"`
}

// toolSchema samples the source and returns the inferred JSON Schema itself as
// the structured result, the same document `iq schema` prints.
func (s *mcpServer) toolSchema(ctx context.Context, _ *mcp.CallToolRequest, in mcpSchemaInput) (*mcp.CallToolResult, map[string]any, error) {
	sample, err := sampleSize(in.Sample)
	if err != nil {
		return nil, nil, toolError(err)
	}
	cfg := s.callConfig()
	if err := resolveInspectSource(cfg, in.Source); err != nil {
		return nil, nil, toolError(err)
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.timeout)
	defer cancel()
	st, err := openStore(cctx, cfg)
	if err != nil {
		return nil, nil, toolError(redactErr(err, cfg.url))
	}
	defer func() { _ = st.Close() }()
	items, err := sampleItems(cctx, st, in.Filter, sample, cfg.runOptions())
	if err != nil {
		return nil, nil, toolError(fmt.Errorf("sample %q: %w", cfg.handle, redactErr(asSyntaxError(in.Filter, err), cfg.url)))
	}
	return nil, shape.Infer(items).JSONSchema(schemaTitle(cfg)), nil
}

// mcpDiffInput is a comparison request: the two sides, the layers to compare, and
// the scoping a diff accepts.
type mcpDiffInput struct {
	A       string   `json:"a" jsonschema:"the left source handle, optionally handle.keyspace"`
	B       string   `json:"b" jsonschema:"the right source handle, optionally handle.keyspace"`
	Data    bool     `json:"data,omitempty" jsonschema:"diff items key by key; the default when no layer is chosen"`
	Stats   bool     `json:"stats,omitempty" jsonschema:"diff native introspection trees; same driver only"`
	Schema  bool     `json:"schema,omitempty" jsonschema:"diff an inferred field/type shape; cross-driver"`
	Filter  string   `json:"filter,omitempty" jsonschema:"a .[]-rooted jq filter scoping both sides"`
	Section []string `json:"section,omitempty" jsonschema:"introspection sections for the stats layer"`
	Sample  *int     `json:"sample,omitempty" jsonschema:"max items sampled per side for the schema layer; omit for 1000, 0 samples all"`
}

// mcpDiffOutput is the per-layer delta, plus whether the sides differ at all (the
// fact `iq diff` reports through its exit status).
type mcpDiffOutput struct {
	Data   []mcpItemDelta `json:"data,omitempty"`
	Stats  []mcpChange    `json:"stats,omitempty"`
	Schema []mcpChange    `json:"schema,omitempty"`
	Differ bool           `json:"differ"`
}

// mcpItemDelta is one keyed item's delta. It projects diff.ItemDelta with the op
// named rather than numbered: the core's Op marshals to its name but is a Go
// integer, so a schema inferred from it would promise the wrong type.
type mcpItemDelta struct {
	Key     string      `json:"key"`
	Op      string      `json:"op" jsonschema:"add, remove or change"`
	Changes []mcpChange `json:"changes,omitempty"`
	Old     any         `json:"old,omitempty"`
	New     any         `json:"new,omitempty"`
}

// mcpChange is one field-level delta, the same projection of diff.Change.
type mcpChange struct {
	Path []string `json:"path"`
	Op   string   `json:"op" jsonschema:"add, remove or change"`
	Old  any      `json:"old,omitempty"`
	New  any      `json:"new,omitempty"`
}

// mcpChanges projects the core's tree deltas into the response shape.
func mcpChanges(in []diff.Change) []mcpChange {
	out := make([]mcpChange, 0, len(in))
	for _, c := range in {
		out = append(out, mcpChange{Path: c.Path, Op: c.Op.String(), Old: c.Old, New: c.New})
	}
	return out
}

// mcpItemDeltas projects the core's keyed deltas into the response shape.
func mcpItemDeltas(in []diff.ItemDelta) []mcpItemDelta {
	out := make([]mcpItemDelta, 0, len(in))
	for _, d := range in {
		out = append(out, mcpItemDelta{
			Key: d.Key, Op: d.Op.String(), Changes: mcpChanges(d.Changes), Old: d.Old, New: d.New,
		})
	}
	return out
}

// toolDiff compares two sources through the same per-layer collectors `iq diff`
// uses, so the deltas are identical to the CLI's --json rendering.
func (s *mcpServer) toolDiff(ctx context.Context, _ *mcp.CallToolRequest, in mcpDiffInput) (*mcp.CallToolResult, mcpDiffOutput, error) {
	sample, err := sampleSize(in.Sample)
	if err != nil {
		return nil, mcpDiffOutput{}, toolError(err)
	}
	cf, err := iqconfig.Load()
	if err != nil {
		return nil, mcpDiffOutput{}, toolError(err)
	}
	left, err := parseSourceSpec(cf, in.A, in.Filter)
	if err != nil {
		return nil, mcpDiffOutput{}, toolError(err)
	}
	right, err := parseSourceSpec(cf, in.B, in.Filter)
	if err != nil {
		return nil, mcpDiffOutput{}, toolError(err)
	}
	data, stats, schema := in.Data, in.Stats, in.Schema
	if !stats && !schema {
		data = true
	}
	if stats && eitherFiltered(left, right) {
		return nil, mcpDiffOutput{}, toolError(errors.New("the stats layer diffs the backend's own introspection, which has no items to filter; drop the filter or diff data/schema"))
	}
	cfg := s.callConfig()
	cctx, cancel := context.WithTimeout(ctx, s.cfg.timeout)
	defer cancel()

	var out mcpDiffOutput
	if data {
		deltas, err := diffRun{cfg: cfg, left: left, right: right}.diffData(cctx, func(int) {})
		if err != nil {
			return nil, mcpDiffOutput{}, toolError(err)
		}
		out.Data = mcpItemDeltas(deltas)
	}
	if stats {
		changes, err := diffRun{left: left, right: right, sections: in.Section}.diffStats(cctx)
		if err != nil {
			return nil, mcpDiffOutput{}, toolError(err)
		}
		out.Stats = mcpChanges(changes)
	}
	if schema {
		changes, err := diffRun{cfg: cfg, left: left, right: right, sample: sample}.diffSchema(cctx)
		if err != nil {
			return nil, mcpDiffOutput{}, toolError(err)
		}
		out.Schema = mcpChanges(changes)
	}
	out.Differ = len(out.Data)+len(out.Stats)+len(out.Schema) > 0
	return nil, out, nil
}

// mcpExecInput carries the backend command verbatim: the verb and its operands,
// exactly as `iq exec` forwards them.
type mcpExecInput struct {
	Source string   `json:"source,omitempty" jsonschema:"the source handle to run against, optionally handle.keyspace; omit for the active source"`
	Args   []string `json:"args" jsonschema:"the command in the backend's own language: a Redis verb and operands, or a single statement or JSON document"`
}

// mcpExecOutput is the backend's reply, normalized to JSON.
type mcpExecOutput struct {
	Result any `json:"result"`
}

// toolExec forwards the command verbatim through the same runner `iq exec` uses.
func (s *mcpServer) toolExec(ctx context.Context, _ *mcp.CallToolRequest, in mcpExecInput) (*mcp.CallToolResult, mcpExecOutput, error) {
	if len(in.Args) == 0 {
		return nil, mcpExecOutput{}, toolError(errors.New("exec: a command is required"))
	}
	cfg := s.callConfig()
	cfg.src = in.Source
	if err := resolveSource(cfg); err != nil {
		return nil, mcpExecOutput{}, toolError(err)
	}
	// Log only the verb: operands may carry data values.
	cfg.log().Info("exec forward", "verb", in.Args[0])
	cctx, cancel := context.WithTimeout(ctx, s.cfg.timeout)
	defer cancel()
	st, err := openStore(cctx, cfg)
	if err != nil {
		return nil, mcpExecOutput{}, toolError(redactErr(err, cfg.url))
	}
	defer func() { _ = st.Close() }()
	res, err := query.NewRunner(st).Run(cctx, in.Args)
	if err != nil {
		return nil, mcpExecOutput{}, toolError(redactErr(err, cfg.url))
	}
	return nil, mcpExecOutput{Result: res}, nil
}

// mcpInsertInput is a copy request: the read side, the destination, the key
// derivation, and the write mode.
type mcpInsertInput struct {
	Source      string `json:"source" jsonschema:"the source handle to read, optionally handle.keyspace"`
	Destination string `json:"destination" jsonschema:"the destination source handle to write into"`
	Filter      string `json:"filter,omitempty" jsonschema:"a jq filter transforming each item before it is written"`
	Key         string `json:"key,omitempty" jsonschema:"a jq expression yielding each written item's key"`
	KeyField    string `json:"key_field,omitempty" jsonschema:"an object field to take the key from"`
	KeyPrefix   string `json:"key_prefix,omitempty" jsonschema:"a string prepended to every written key"`
	Type        string `json:"type,omitempty" jsonschema:"the native type stamped on a reshaped value, e.g. hash, list, json"`
	NoOverwrite *bool  `json:"no_overwrite,omitempty" jsonschema:"skip keys that already exist; true by default"`
	Replace     bool   `json:"replace,omitempty" jsonschema:"empty the destination before writing; needs --allow destructive and a confirmation"`
	DryRun      bool   `json:"dry_run,omitempty" jsonschema:"report the effect without writing anything"`
	Confirm     bool   `json:"confirm,omitempty" jsonschema:"confirm a replace, which destroys the destination's data"`
}

// insertTransformOptions maps the tool's reshaping inputs onto the core's
// transform options, the mapping the CLI's --filter, --key, --key-field,
// --key-prefix and --type flags perform.
func insertTransformOptions(in mcpInsertInput) query.TransformOptions {
	return query.TransformOptions{
		Filter: in.Filter, Key: in.Key, KeyField: in.KeyField, KeyPrefix: in.KeyPrefix, Type: in.Type,
	}
}

// insertRequestFrom states the tool inputs as the write request both delivery
// mechanisms share. no_overwrite is on unless the call turns it off, and the
// confirmation was answered in protocol before this point, so the request never
// prompts; --force has no meaning on this surface.
func insertRequestFrom(in mcpInsertInput) insertRequest {
	mode := query.InsertOnly
	if !boolOr(in.NoOverwrite, true) {
		mode = query.Upsert
	}
	return insertRequest{
		dst: in.Destination, mode: mode, replace: in.Replace, dryRun: in.DryRun,
		confirm: func(string) error { return nil },
	}
}

// mcpWriteOutput reports a write's honest accounting.
type mcpWriteOutput struct {
	Destination string `json:"destination"`
	Written     int    `json:"written"`
	Overwritten int    `json:"overwritten"`
	Skipped     int    `json:"skipped"`
	DryRun      bool   `json:"dry_run"`
}

// toolInsert copies a source's items into a destination through the same write
// path `iq --insert` drives. replace empties the destination first, so it needs
// the destructive capability and a confirmation.
func (s *mcpServer) toolInsert(ctx context.Context, req *mcp.CallToolRequest, in mcpInsertInput) (*mcp.CallToolResult, mcpWriteOutput, error) {
	if strings.TrimSpace(in.Source) == "" {
		return nil, mcpWriteOutput{}, toolError(errors.New("insert: a source is required"))
	}
	if strings.TrimSpace(in.Destination) == "" {
		return nil, mcpWriteOutput{}, toolError(errors.New("insert: a destination is required"))
	}
	if in.Replace && !s.allows(allowDestructive) {
		return nil, mcpWriteOutput{}, toolError(errors.New("replace empties the destination, so it needs the destructive capability; the server was started without --allow destructive"))
	}
	if in.Replace && !in.DryRun {
		if res, ok := s.confirmation(req, in.Confirm, fmt.Sprintf("empty %s before writing", in.Destination)); !ok {
			return res, mcpWriteOutput{}, nil
		}
	}
	transform, err := query.NewTransform(insertTransformOptions(in))
	if err != nil {
		return nil, mcpWriteOutput{}, toolError(err)
	}

	cfg := s.callConfig()
	cfg.src = in.Source
	if err := resolveSource(cfg); err != nil {
		return nil, mcpWriteOutput{}, toolError(err)
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.timeout)
	defer cancel()
	st, err := openStore(cctx, cfg)
	if err != nil {
		return nil, mcpWriteOutput{}, toolError(redactErr(err, cfg.url))
	}
	defer func() { _ = st.Close() }()
	tr, ok := st.(query.TypedReader)
	if !ok {
		return nil, mcpWriteOutput{}, toolError(fmt.Errorf("source %s (%s) cannot be read for a move", cfg.handle, driverName(cfg.url)))
	}

	label, stat, err := applyInsert(cctx, insertRequestFrom(in), tr.TypedScan, transform)
	if err != nil {
		return nil, mcpWriteOutput{}, toolError(err)
	}
	return nil, mcpWriteOutput{
		Destination: label, Written: stat.Written, Overwritten: stat.Overwritten,
		Skipped: stat.Skipped, DryRun: in.DryRun,
	}, nil
}

// mcpLifecycleInput names the container to empty or drop.
type mcpLifecycleInput struct {
	Source  string `json:"source" jsonschema:"the target source handle, optionally handle.keyspace"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"report the effect without changing anything"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"confirm the operation, which destroys the stored data"`
}

// mcpLifecycleOutput reports what the operation did, in the same words the CLI
// prints.
type mcpLifecycleOutput struct {
	Target  string `json:"target"`
	Outcome string `json:"outcome"`
	DryRun  bool   `json:"dry_run"`
}

// toolDataClear empties a container, keeping it.
func (s *mcpServer) toolDataClear(ctx context.Context, req *mcp.CallToolRequest, in mcpLifecycleInput) (*mcp.CallToolResult, mcpLifecycleOutput, error) {
	return s.runLifecycleTool(ctx, req, in, clearOp)
}

// toolDataDrop removes a container entirely.
func (s *mcpServer) toolDataDrop(ctx context.Context, req *mcp.CallToolRequest, in mcpLifecycleInput) (*mcp.CallToolResult, mcpLifecycleOutput, error) {
	return s.runLifecycleTool(ctx, req, in, dropOp)
}

// runLifecycleTool is the shared body of the clear and drop tools: resolve the
// target, take the confirmation unless the call is a dry run, then apply the op
// through the same implementation `iq data` drives.
func (s *mcpServer) runLifecycleTool(ctx context.Context, req *mcp.CallToolRequest, in mcpLifecycleInput, op lifecycleOp) (*mcp.CallToolResult, mcpLifecycleOutput, error) {
	t, err := s.lifecycleTarget(in.Source, op.name)
	if err != nil {
		return nil, mcpLifecycleOutput{}, toolError(err)
	}
	if !in.DryRun {
		if res, ok := s.confirmation(req, in.Confirm, fmt.Sprintf("%s %s", op.name, t.label())); !ok {
			return res, mcpLifecycleOutput{}, nil
		}
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.timeout)
	defer cancel()
	line, err := applyLifecycleOp(cctx, op, t, in.DryRun, func(string) error { return nil })
	if err != nil {
		return nil, mcpLifecycleOutput{}, toolError(err)
	}
	return nil, mcpLifecycleOutput{Target: t.label(), Outcome: line, DryRun: in.DryRun}, nil
}

// mcpDeleteInput names the container and the keys to remove from it.
type mcpDeleteInput struct {
	Source  string   `json:"source" jsonschema:"the target source handle, optionally handle.keyspace"`
	Keys    []string `json:"keys" jsonschema:"the keys to remove, each spelled as a Get spells it: a bare string, or a JSON array for a composite key"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"report the effect without changing anything"`
	Confirm bool     `json:"confirm,omitempty" jsonschema:"confirm the operation, which destroys the stored data"`
}

// toolDataDelete removes a named set of keys, keeping the container.
func (s *mcpServer) toolDataDelete(ctx context.Context, req *mcp.CallToolRequest, in mcpDeleteInput) (*mcp.CallToolResult, mcpLifecycleOutput, error) {
	keys, err := dedupeKeys(in.Keys)
	if err != nil {
		return nil, mcpLifecycleOutput{}, toolError(err)
	}
	if len(keys) == 0 {
		return nil, mcpLifecycleOutput{}, toolError(errors.New("delete: at least one key is required"))
	}
	t, err := s.lifecycleTarget(in.Source, "delete")
	if err != nil {
		return nil, mcpLifecycleOutput{}, toolError(err)
	}
	if !in.DryRun {
		if res, ok := s.confirmation(req, in.Confirm, fmt.Sprintf("delete %d key(s) from %s", len(keys), t.label())); !ok {
			return res, mcpLifecycleOutput{}, nil
		}
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.timeout)
	defer cancel()
	line, err := deleteKeys(cctx, t, keys, in.DryRun)
	if err != nil {
		return nil, mcpLifecycleOutput{}, toolError(err)
	}
	return nil, mcpLifecycleOutput{Target: t.label(), Outcome: line, DryRun: in.DryRun}, nil
}

// lifecycleTarget resolves a destructive tool's target through the registry
// before any connection opens. A file endpoint is refused: a dump has no
// container to empty, drop or delete from.
func (s *mcpServer) lifecycleTarget(arg, op string) (endpoint, error) {
	if strings.TrimSpace(arg) == "" {
		return endpoint{}, fmt.Errorf("%s: a source is required", op)
	}
	cf, err := iqconfig.Load()
	if err != nil {
		return endpoint{}, err
	}
	t, err := resolveEndpoint(cf, arg, false)
	if err != nil {
		return endpoint{}, err
	}
	if t.isFile {
		return endpoint{}, fmt.Errorf("%s needs a source, not a file: %q", op, arg)
	}
	return t, nil
}

// confirmation gates a destructive call. It returns ok when the call may proceed:
// either the caller passed confirm, or a previous round trip came back with the
// user accepting. Otherwise it returns the result to send back — an input-required
// result carrying the confirmation when the client can elicit one (the 2026-07-28
// multi round-trip mechanism, which the SDK also fulfills for older clients), and
// an error result naming what to pass when it cannot.
func (s *mcpServer) confirmation(req *mcp.CallToolRequest, confirmed bool, action string) (*mcp.CallToolResult, bool) {
	if confirmed {
		return nil, true
	}
	if approved, answered := confirmationAnswer(req); answered {
		if approved {
			return nil, true
		}
		return errorResult(fmt.Errorf("%s: declined", action)), false
	}
	if caps := req.ClientCapabilities(); caps == nil || caps.Elicitation == nil {
		return errorResult(fmt.Errorf("%s: destroys data and needs confirmation; re-run this call with confirm: true", action)), false
	}
	return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{
		// No Mode: the SDK infers "form" when no URL is set.
		confirmationID: &mcp.ElicitParams{
			Message: action + "? This destroys data and cannot be undone.",
			RequestedSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"confirm": map[string]any{
						"type":        "boolean",
						"description": "true to proceed, false to abort",
					},
				},
				"required": []string{"confirm"},
			},
		},
	}}, false
}

// confirmationID names the confirmation in the input-request map the client
// echoes back.
const confirmationID = "confirm"

// confirmationAnswer reads a confirmation the client fulfilled on a previous
// round trip. It reports whether an answer is present and, if so, whether it
// approves. Anything but an accepted form carrying confirm true is a refusal.
func confirmationAnswer(req *mcp.CallToolRequest) (approved, answered bool) {
	if req == nil || req.Params == nil {
		return false, false
	}
	resp, ok := req.Params.InputResponses[confirmationID]
	if !ok {
		return false, false
	}
	er, ok := resp.(*mcp.ElicitResult)
	if !ok || er.Action != "accept" {
		return false, true
	}
	v, _ := er.Content["confirm"].(bool)
	return v, true
}

// errorResult renders err as a tool result in the CLI's JSON error shape. It is
// the form used where a handler must return a result rather than an error, so the
// two paths produce the same document.
func errorResult(err error) *mcp.CallToolResult {
	var res mcp.CallToolResult
	res.SetError(toolError(err))
	return &res
}

// progressHooks turns the request's progress token, when it carries one, into the
// engine's per-page and estimate callbacks, so a long scan reports on the
// request's own stream. Without a token both are nil and the engine calls
// nothing.
func (s *mcpServer) progressHooks(ctx context.Context, req *mcp.CallToolRequest) (onPage func(int), onEstimate func(int64)) {
	if req == nil || req.Params == nil || req.Session == nil {
		return nil, nil
	}
	token := req.Params.GetProgressToken()
	if token == nil {
		return nil, nil
	}
	var scanned, total float64
	notify := func() {
		_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: token, Progress: scanned, Total: total, Message: "scanning",
		})
	}
	return func(n int) {
			scanned += float64(n)
			notify()
		}, func(n int64) {
			total = float64(n)
			notify()
		}
}

// callTimeout resolves one call's deadline: the server's --timeout, lowered by a
// per-call duration when the caller passes a shorter one. A per-call value may
// only tighten the bound, never loosen it.
func (s *mcpServer) callTimeout(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return s.cfg.timeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout %q: want a duration such as 10s", raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid timeout %q: want a positive duration", raw)
	}
	return min(d, s.cfg.timeout), nil
}

// capValue resolves one per-call cap against the server's own: unset takes the
// server's, a positive value lowers it, and a value above it is clamped back
// down. A negative value is bad input and is refused at entry.
func capValue(name string, want, limit int) (int, error) {
	if want < 0 {
		return 0, fmt.Errorf("invalid %s %d: want a positive count", name, want)
	}
	if want == 0 {
		return limit, nil
	}
	return min(want, limit), nil
}

// defaultSchemaSample mirrors the --sample default of `iq schema` and
// `iq diff --schema`, so an inferred shape is sampled the same width on both
// surfaces.
const defaultSchemaSample = 1000

// sampleSize resolves a sampling cap: an omitted value takes the CLI's own
// default, an explicit zero samples everything, and a negative count is bad
// input. It is the one place that default lives for the schema and diff tools.
func sampleSize(v *int) (int, error) {
	if v == nil {
		return defaultSchemaSample, nil
	}
	if *v < 0 {
		return 0, fmt.Errorf("invalid sample %d: want a non-negative item count", *v)
	}
	return *v, nil
}

// boolOr returns the pointed-to value, or def when the caller left the field out.
// A tri-state pointer is how an optional boolean whose default is true stays
// distinguishable from an explicit false.
func boolOr(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

// decodeJSONObject parses a rendered JSON document into a plain map, the shape a
// structured tool result carries. It is how the introspection renderers, which
// write JSON, feed a structured result without a second rendering path.
func decodeJSONObject(data []byte) (map[string]any, error) {
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode introspection: %w", err)
	}
	return out, nil
}

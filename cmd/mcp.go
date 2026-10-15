package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// Capability names that --allow accepts. The set is closed, so a misspelling fails
// at command entry and does not silently leave a tool unregistered. Each name gates
// a group of tools. The writes name gates the --insert copy. The exec name gates
// the verbatim backend command. The destructive name gates the container lifecycle
// (and --replace, which empties a destination).
const (
	allowWrites      = "writes"
	allowExec        = "exec"
	allowDestructive = "destructive"
)

// allowNames is the closed --allow value set, in the order --help and the
// completion offer it.
var allowNames = []string{allowWrites, allowExec, allowDestructive}

// Server-side result caps, the defaults for --max-items and --max-bytes. The
// values are 200 items and 256 KiB. That is roughly the upper end of what a tool
// result is meant to inject into the context of an agent. A per-call value can
// lower them. It can never raise them.
const (
	defaultMCPMaxItems = 200
	defaultMCPMaxBytes = 256 * 1024
)

// mcpListTTLMs is the tools/list cache hint. --allow fixes the tool set at
// process start, so only a restart changes the list. An hour is an honest
// freshness window.
const mcpListTTLMs = 3600_000

// mcpServer is the delivery mechanism that `iq mcp` builds: the inherited run
// config, the hard result caps, and the capability set that --allow opened. It
// holds no protocol state. The tool set is fixed at process start, and every call
// resolves its own source and opens its own store. So concurrent calls share
// nothing mutable.
type mcpServer struct {
	cfg      *config
	maxItems int
	maxBytes int
	allow    map[string]bool
}

// allows reports whether the named capability was opened with --allow.
func (s *mcpServer) allows(name string) bool { return s.allow[name] }

// callConfig returns a fresh per-call config carrying the run-wide settings the
// core needs (timeout, decimal mode, logger, cache flags, pushdown). The server's
// own config is never mutated, so two concurrent tool calls cannot race on the
// source each resolved.
func (s *mcpServer) callConfig() *config {
	return &config{
		timeout:      s.cfg.timeout,
		decimalMode:  s.cfg.decimalMode,
		logger:       s.cfg.logger,
		noCache:      s.cfg.noCache,
		noCacheIndex: s.cfg.noCacheIndex,
		noCompile:    s.cfg.noCompile,
	}
}

// mcpInstructions is the guidance of the server to a connected client, a
// condensed form of the workflow and rules of the agent skill. It is the one
// place that an agent reads before its first call. So it states the plan-first
// habit, the bounds, and what the destructive tools cost.
const mcpInstructions = `iq runs a jq filter against a saved NoSQL source (Redis, MongoDB, Cassandra,
DynamoDB, Elasticsearch, OpenSearch, CouchDB, Couchbase, HBase, Neo4j) or a dump
file, and normalizes every backend's values to one JSON, so a filter means the
same thing everywhere.

Workflow:
1. iq_sources lists the handles you may reach. Address one as "<handle>" or
   "<handle>.<keyspace>" (collection, table, index, label). Never guess a handle.
2. iq_explain before any scan: it never connects, and it names the route
   (bounded read, streaming scan, materialized scan) and the stages planned for
   each select() conjunct (server, prefilter, jq).
3. iq_query runs the filter. The filter is both the key selector and the
   transform: '.["k"]' fetches one key, '.[] | select(...)' streams the keyspace.
4. iq_inspect and iq_schema describe a source without dumping it; iq_diff
   compares two.

Rules:
- A filter that needs the whole keyspace at once (., keys, length, group_by,
  limit(), first(), slices) is refused unless you pass unbounded: true. Ask the
  user before you do: it loads the whole keyspace into memory.
- Results are capped by max_items and max_bytes; truncated: true means there was
  more. Project fields ({id, total}) and put equality tests in select() so they
  push to the backend.
- A missing key reads as null, never an error. Redis strings stay strings
  (tonumber before arithmetic); sets are sorted arrays; sorted sets are
  [{member, score}].
- Streamed output is in scan order and may repeat an item if the keyspace
  resizes mid-scan.
- Say what a call costs before running it on production-sized data: a DynamoDB
  scan reads and bills the whole table, a Cassandra pushdown may run with
  ALLOW FILTERING.
- Write and destructive tools appear only when the operator started the server
  with --allow. Preview a write with dry_run: true first, prefer
  no_overwrite: true, and never confirm a destructive call on your own.`

// newMCPCmd builds `iq mcp`: the query core served as a Model Context Protocol
// server over stdio. It is a second thin delivery mechanism beside the CLI
// over the same core. It is read-only by default. --allow opens the write,
// exec and destructive tools. A tool that is not allowed is not registered, so
// it is absent from tools/list.
func newMCPCmd(cfg *config) *cobra.Command {
	var (
		allow    []string
		maxItems int
		maxBytes int
	)
	c := &cobra.Command{
		Use: "mcp",
		// The command takes no positionals, and its own name is not a file, so
		// suppress the shell's filename fallback.
		ValidArgsFunction: cobra.NoFileCompletions,
		Short:             "Serve the query core as an MCP server over stdio",
		Long: "Serve iq to an AI agent as a Model Context Protocol server, speaking JSON-RPC\n" +
			"over stdin and stdout. The tools are the CLI's own operations over the same\n" +
			"core: iq_sources, iq_ping, iq_explain, iq_query, iq_inspect, iq_schema and\n" +
			"iq_diff.\n" +
			"\n" +
			"Read-only by default. --allow writes registers iq_insert, --allow exec\n" +
			"registers iq_exec, and --allow destructive registers iq_data_clear,\n" +
			"iq_data_drop and iq_data_delete (and permits iq_insert's replace). A tool that\n" +
			"is not allowed is never registered, so a client cannot see it or call it. A\n" +
			"real call to iq_data_clear, iq_data_drop, iq_data_delete or iq_insert's replace\n" +
			"needs confirm: true, or the client's user answering the confirmation the\n" +
			"server asks for. iq_exec has no preview and no confirmation: with --allow exec\n" +
			"the agent can do anything the database account can do, so give the agent a\n" +
			"read-only database user unless it must write.\n" +
			"\n" +
			"Every result is capped by --max-items and --max-bytes; a per-call max_items or\n" +
			"max_bytes may lower them, never raise them. The persistent flags apply:\n" +
			"--timeout bounds every call (5s is short for a scan, so a client config should\n" +
			"pass --timeout 30s), --config picks the source registry, and --log* write\n" +
			"diagnostics to a file. stdout carries the protocol and nothing else.\n" +
			"\n" +
			"The server inherits the saved sources and the keyring, so point an agent at a\n" +
			"dedicated config holding only the sources it may reach.",
		Example: "  # Register the server with a client (Claude Code, Codex).\n" +
			"  $ claude mcp add iq -- iq mcp --timeout 30s\n" +
			"  $ codex mcp add iq -- iq mcp --timeout 30s\n" +
			"\n" +
			"  # Point the agent at a dedicated config holding only what it may reach.\n" +
			"  $ iq mcp --config ~/.config/iq/agent.toml --timeout 30s\n" +
			"\n" +
			"  # Open the write tools, then also the destructive ones.\n" +
			"  $ iq mcp --allow writes\n" +
			"  $ iq mcp --allow writes --allow destructive\n" +
			"\n" +
			"  # Tighten the result caps for a small context window.\n" +
			"  $ iq mcp --max-items 50 --max-bytes 65536",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Validation only, in a fixed order: the log sink, the caps, then the
			// capability set. Nothing here opens a store or a stream.
			if err := refuseStdoutLog(cmd, cfg); err != nil {
				return err
			}
			if maxItems <= 0 {
				return fmt.Errorf("invalid --max-items %d: want a positive item count", maxItems)
			}
			if maxBytes <= 0 {
				return fmt.Errorf("invalid --max-bytes %d: want a positive byte count", maxBytes)
			}
			allowed, err := parseAllow(allow)
			if err != nil {
				return err
			}
			// stdout is the protocol channel. The spinner shares no stream with it,
			// but it repaints over stderr on a run that must stay quiet. Plan text
			// is rendered into a JSON result, where an ANSI escape is noise.
			cfg.noProgress = true
			resolveColor(true, false, io.Discard)
			s := &mcpServer{cfg: cfg, maxItems: maxItems, maxBytes: maxBytes, allow: allowed}
			return s.serve(cmd.Context(), &mcp.StdioTransport{})
		},
	}
	c.Flags().StringArrayVar(&allow, "allow", nil, "open a capability's tools: writes (iq_insert), exec (iq_exec), or destructive (iq_data_clear/drop/delete, and iq_insert's replace); repeatable, read-only without it")
	c.Flags().IntVar(&maxItems, "max-items", defaultMCPMaxItems, "hard cap on the items one tool result carries; a per-call max_items may only lower it")
	c.Flags().IntVar(&maxBytes, "max-bytes", defaultMCPMaxBytes, "hard cap in bytes on one tool result's encoded items; a per-call max_bytes may only lower it")
	_ = c.RegisterFlagCompletionFunc("allow", fixedValues(allowNames...))
	return c
}

// refuseStdoutLog rejects a log sink pointed at stdout. stdout carries the MCP
// protocol, so a log record written there corrupts every frame after it. A
// client then sees a parse error and not a diagnostic. The sink is re-resolved
// through the same function that PersistentPreRunE used, which validates and
// opens nothing. So this reads exactly the target that is about to be used.
func refuseStdoutLog(cmd *cobra.Command, cfg *config) error {
	o, err := resolveLogOptions(cmd, cfg)
	if err != nil {
		return err
	}
	// Matched exactly as logSink matches it. Any other spelling is a filesystem
	// path, not the stream, and refusing one refuses a legitimate log file.
	if o.fileActive() && o.file == "stdout" {
		return errors.New("iq mcp writes the protocol to stdout, so it cannot also log there; point --log.file at a path or at stderr")
	}
	return nil
}

// parseAllow validates the --allow values against the closed capability set and
// returns them as a lookup set. An unknown value is rejected at command entry,
// before any registry or store work, so a typo never silently withholds a tool.
func parseAllow(values []string) (map[string]bool, error) {
	allowed := make(map[string]bool, len(values))
	for _, v := range values {
		name := strings.ToLower(strings.TrimSpace(v))
		if !slices.Contains(allowNames, name) {
			return nil, fmt.Errorf("invalid --allow %q: want %s", v, strings.Join(allowNames, ", "))
		}
		allowed[name] = true
	}
	return allowed, nil
}

// serve builds the MCP server and runs it on the transport until the peer
// disconnects or the context is canceled.
func (s *mcpServer) serve(ctx context.Context, t mcp.Transport) error {
	srv := s.newServer()
	if err := srv.Run(ctx, t); err != nil {
		// A canceled context is the operator stopping the server, not a failure.
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("serve mcp over stdio: %w", err)
	}
	return nil
}

// newServer assembles the MCP server: the identity, the instructions, the
// tools that --allow permits, and the tools/list cache hint. It
// advertises the tools capability only. Logging, roots and sampling are
// deprecated as of the 2026-07-28 revision, and iq needs none of them.
func (s *mcpServer) newServer() *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "iq", Title: "iq", Version: buildVersion()},
		&mcp.ServerOptions{
			Instructions: mcpInstructions,
			Logger:       s.cfg.log(),
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		},
	)
	s.addTools(srv)
	srv.AddReceivingMiddleware(listToolsCacheHint)
	return srv
}

// listToolsCacheHint stamps the tools/list result with the cache hint that the
// 2026-07-28 revision expects. The scope is private. The list is exactly what the
// --allow set of this process opened, so no intermediary is allowed to serve it
// to another user.
func listToolsCacheHint(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if err != nil {
			return nil, err
		}
		if lt, ok := res.(*mcp.ListToolsResult); ok {
			lt.Cacheable = mcp.Cacheable{TTLMs: mcpListTTLMs, CacheScope: "private"}
		}
		return res, nil
	}
}

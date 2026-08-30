package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	iqfile "github.com/zsltg/iq/drivers/file"
)

// tappedPipe wraps the server's stdout so the test can both feed it to the MCP
// client and, afterwards, inspect every byte the process wrote. stdout is the
// protocol channel, so a stray print would corrupt the stream, and this is how
// the test proves none happened.
type tappedPipe struct {
	r  io.ReadCloser
	mu sync.Mutex
	b  bytes.Buffer
}

// Read forwards the pipe and records what it returned.
func (t *tappedPipe) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.mu.Lock()
		t.b.Write(p[:n])
		t.mu.Unlock()
	}
	return n, err
}

// Close closes the underlying pipe.
func (t *tappedPipe) Close() error { return t.r.Close() }

// written returns everything the server has written to stdout so far.
func (t *tappedPipe) written() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.b.String()
}

// startMCP launches `iq mcp` as a long-lived child process and connects an MCP
// client to it over its real stdin and stdout, the way a client such as Claude
// Code or Codex does. It returns the live session, the stdout tap, and the
// process's stderr, all torn down on cleanup.
func startMCP(t *testing.T, env []string, args ...string) (*mcp.ClientSession, *tappedPipe, *bytes.Buffer) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), iqBin, append([]string{"mcp"}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())

	tap := &tappedPipe{r: stdout}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "iq-e2e", Version: "v0"}, nil).
		Connect(t.Context(), &mcp.IOTransport{Reader: tap, Writer: stdin}, nil)
	require.NoErrorf(t, err, "connect to iq mcp: %s", stderr.String())

	t.Cleanup(func() {
		_ = cs.Close()
		// Closing stdin ends the server's read loop, so it exits on its own; the
		// context kill is the backstop for a server that does not.
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	return cs, tap, &stderr
}

// TestMCPStdioSession drives the built binary as an MCP server over real stdio:
// list the tools, run a query against a file:// dump, and prove that the write
// tool is absent and that stdout carried nothing but protocol frames.
func TestMCPStdioSession(t *testing.T) {
	skipShort(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "data.jsonl")
	require.NoError(t, os.WriteFile(data,
		[]byte(`{"key":"alpha","value":"hello-iq"}`+"\n"+`{"key":"beta","value":"world-iq"}`+"\n"), 0o600))
	env := []string{"IQ_CONFIG=" + filepath.Join(dir, "iq.toml")}
	mustRun(t, env, "add", iqfile.URL(data), "-n", "snap")

	cs, tap, stderr := startMCP(t, env, "--timeout", "30s")

	// The server identifies itself before any call.
	require.Equal(t, "iq", cs.InitializeResult().ServerInfo.Name)
	require.NotEmpty(t, cs.InitializeResult().Instructions)

	list, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	names := make([]string, 0, len(list.Tools))
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	require.Contains(t, names, "iq_query")
	require.Contains(t, names, "iq_explain")
	require.Contains(t, names, "iq_sources")
	require.NotContains(t, names, "iq_insert", "the write tool needs --allow writes")
	require.NotContains(t, names, "iq_exec", "the exec tool needs --allow exec")
	require.NotContains(t, names, "iq_data_drop", "the lifecycle tools need --allow destructive")

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "iq_query",
		Arguments: map[string]any{"source": "snap", "filter": ".[]"},
	})
	require.NoError(t, err)
	require.Falsef(t, res.IsError, "iq_query failed: %v", res.Content)
	var out struct {
		Items     []any `json:"items"`
		Count     int   `json:"count"`
		Truncated bool  `json:"truncated"`
	}
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, 2, out.Count)
	require.False(t, out.Truncated)
	require.Contains(t, out.Items, "hello-iq")
	require.Contains(t, out.Items, "world-iq")

	// A tool that was never registered is unknown to the server, not merely
	// refused, so calling it is a protocol error.
	_, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "iq_insert", Arguments: map[string]any{}})
	require.Error(t, err)

	require.NoError(t, cs.Close())
	assertProtocolOnly(t, tap.written())
	require.Empty(t, stderr.String(), "diagnostics must not be written without --log or --verbose")
}

// assertProtocolOnly proves every line the server wrote to stdout is a JSON-RPC
// frame: one stray fmt.Print would desynchronize the client.
func assertProtocolOnly(t *testing.T, out string) {
	t.Helper()
	require.NotEmpty(t, out, "the server wrote nothing to stdout")
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		var frame map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(line), &frame), "stdout carried a non-JSON line: %q", line)
		require.Equalf(t, "2.0", frame["jsonrpc"], "stdout carried a non-JSON-RPC object: %q", line)
	}
}

// TestMCPStdioAllowRegistersWriteTools proves --allow is what puts a gated tool
// on the wire, and that an unknown capability stops the server at startup.
func TestMCPStdioAllowRegistersWriteTools(t *testing.T) {
	skipShort(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "data.jsonl")
	require.NoError(t, os.WriteFile(data, []byte(`{"key":"alpha","value":"hello-iq"}`+"\n"), 0o600))
	env := []string{"IQ_CONFIG=" + filepath.Join(dir, "iq.toml")}
	mustRun(t, env, "add", iqfile.URL(data), "-n", "snap")

	t.Run("writes and destructive register their tools", func(t *testing.T) {
		cs, _, _ := startMCP(t, env, "--allow", "writes", "--allow", "destructive")
		list, err := cs.ListTools(t.Context(), nil)
		require.NoError(t, err)
		names := make([]string, 0, len(list.Tools))
		for _, tool := range list.Tools {
			names = append(names, tool.Name)
		}
		require.Contains(t, names, "iq_insert")
		require.Contains(t, names, "iq_data_clear")
		require.Contains(t, names, "iq_data_drop")
		require.Contains(t, names, "iq_data_delete")
		require.NotContains(t, names, "iq_exec", "exec is its own capability")
	})

	t.Run("an unknown capability refuses to start", func(t *testing.T) {
		_, stderr, code := run(t, env, "mcp", "--allow", "root")
		require.NotZero(t, code)
		require.Contains(t, stderr, `invalid --allow "root"`)
	})
}

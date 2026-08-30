package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	iqfile "github.com/zsltg/iq/drivers/file"
)

// iqBin is the path to the binary built once for the package by TestMain.
var iqBin string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// runTests builds iq once for the whole package. flag.Parse must run before
// testing.Short is read, and os.Exit skips deferred cleanup, so the build's temp
// dir is removed here rather than by the caller. Under -short every test skips,
// so the build cost is not paid.
func runTests(m *testing.M) int {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	dir, err := os.MkdirTemp("", "iq-e2e")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: temp dir: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()

	iqBin = filepath.Join(dir, "iq")
	build := exec.CommandContext(context.Background(), "go", "build", "-o", iqBin, ".")
	build.Dir = ".." // the module root, one level above e2e/.
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build iq: %v\n", err)
		return 1
	}
	return m.Run()
}

// skipShort skips the caller under -short, where iqBin was never built.
func skipShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e builds and runs the iq binary; skipped under -short")
	}
}

// run invokes the built binary with extra env vars, returning stdout, stderr, and
// the process exit code (0 on success).
func run(t *testing.T, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runIn(t, "", env, args...)
}

// runIn is run with stdin fed from a string, for the piped-source path.
func runIn(t *testing.T, stdin string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runCtx(t, t.Context(), stdin, env, args...)
}

// runCtx runs the binary under an explicit context. Body steps pass t.Context(); a
// t.Cleanup step must pass context.Background(), since t.Context() is already canceled
// by the time cleanups run.
func runCtx(t *testing.T, ctx context.Context, stdin string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.CommandContext(ctx, iqBin, args...)
	cmd.Env = append(os.Environ(), env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run iq %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

// mustRun runs the binary and fails the test if it exits non-zero, for setup steps
// whose arguments carry no secret (never a raw URL) so echoing them is safe.
func mustRun(t *testing.T, env []string, args ...string) string {
	t.Helper()
	out, stderr, code := run(t, env, args...)
	require.Zerof(t, code, "iq %v failed: %s", args, stderr)
	return out
}

func TestVersionReportsMetadata(t *testing.T) {
	skipShort(t)
	out, _, code := run(t, nil, "version")
	require.Zero(t, code)
	require.Contains(t, out, "commit:")
	require.Contains(t, out, "go:")
}

func TestVersionFlagIsBare(t *testing.T) {
	skipShort(t)
	out, _, code := run(t, nil, "--version")
	require.Zero(t, code)
	require.NotContains(t, out, "iq")
	require.NotContains(t, out, "version")
	require.Regexp(t, `^\S+\n$`, out)
}

func TestHelpListsUsage(t *testing.T) {
	skipShort(t)
	out, _, code := run(t, nil, "--help")
	require.Zero(t, code)
	require.Contains(t, out, "Usage:")
}

func TestUnknownFlagExitsNonZero(t *testing.T) {
	skipShort(t)
	_, stderr, code := run(t, nil, "--nonsense")
	require.NotZero(t, code)
	require.Contains(t, stderr, "unknown flag")
}

func TestOfflineFileSourceRoundTrip(t *testing.T) {
	skipShort(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "data.jsonl")
	require.NoError(t, os.WriteFile(data,
		[]byte(`{"key":"alpha","value":"hello-iq"}`+"\n"+`{"key":"beta","value":"world-iq"}`+"\n"), 0o600))
	env := []string{"IQ_CONFIG=" + filepath.Join(dir, "iq.toml")}

	out, stderr, code := run(t, env, "add", iqfile.URL(data), "-n", "snap")
	require.Zerof(t, code, "add failed: %s", stderr)
	require.Contains(t, out, "added source snap")

	out, stderr, code = run(t, env, "--src", "snap", ".[]")
	require.Zerof(t, code, "query failed: %s", stderr)
	require.Contains(t, out, "hello-iq")
	require.Contains(t, out, "world-iq")
}

func TestStructuredLoggingToStderr(t *testing.T) {
	skipShort(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "data.jsonl")
	require.NoError(t, os.WriteFile(data,
		[]byte(`{"key":"alpha","value":{"total":150}}`+"\n"+`{"key":"beta","value":{"total":10}}`+"\n"), 0o600))
	env := []string{"IQ_CONFIG=" + filepath.Join(dir, "iq.toml")}

	out, stderr, code := run(t, env, "add", iqfile.URL(data), "-n", "snap")
	require.Zerof(t, code, "add failed: %s", stderr)
	require.Contains(t, out, "added source snap")

	// --log.file=stderr implies enable (no --log needed); --log.format=json makes
	// stderr one JSON object per line, parseable in CI. Query output stays on stdout.
	out, stderr, code = run(t, env,
		"--src", "snap", "--log.file=stderr", "--log.format=json",
		".[] | select(.total > 99)")
	require.Zerof(t, code, "query failed: %s", stderr)
	require.Contains(t, out, "150", "the matching value is on stdout")

	// Every stderr line must be a JSON object; index the records by msg.
	byMsg := map[string]map[string]any{}
	for line := range strings.SplitSeq(strings.TrimSpace(stderr), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(line), &rec), "stderr line not JSON: %q", line)
		byMsg[rec["msg"].(string)] = rec
	}

	// The query plan record carries the documented stable keys.
	plan := byMsg["query plan"]
	require.NotNil(t, plan, "a query plan record must be logged")
	require.Equal(t, "snap", plan["handle"])
	require.Equal(t, "file", plan["driver"])
	require.Contains(t, plan, "classification")
	require.Contains(t, plan, "ops")
	cls := plan["classification"].(map[string]any)
	require.Equal(t, true, cls["scan"])
	require.Equal(t, true, cls["streamable"])

	// The runtime scan strategy record proves the engine got a Logger and pushed
	// the predicate down (the file driver pre-filters via FilteredScanner).
	stratRec := byMsg["scan strategy"]
	require.NotNil(t, stratRec, "a scan strategy record must be logged (engine Logger wired)")
	require.Equal(t, true, stratRec["pushed"], "a pushable filter on the file driver must push")

	// The query complete record's scanned count proves the OnPage hook ran.
	done := byMsg["query complete"]
	require.NotNil(t, done, "a query complete record must be logged")
	require.GreaterOrEqual(t, int(done["scanned"].(float64)), 1, "OnPage must have counted scanned items")
}

// TestUnboundedLogsHolisticScan drives a holistic (non-streamable) filter with
// --unbounded through the logging path: it must succeed (proving RunOptions carries
// Unbounded) and emit a query plan record. Without --unbounded the same filter is
// refused, so this pins that the flag reaches the engine.
func TestUnboundedLogsHolisticScan(t *testing.T) {
	skipShort(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "data.jsonl")
	require.NoError(t, os.WriteFile(data,
		[]byte(`{"key":"alpha","value":1}`+"\n"+`{"key":"beta","value":2}`+"\n"), 0o600))
	env := []string{"IQ_CONFIG=" + filepath.Join(dir, "iq.toml")}

	_, stderr, code := run(t, env, "add", iqfile.URL(data), "-n", "snap")
	require.Zerof(t, code, "add failed: %s", stderr)

	// keys is holistic (materializes), so it needs --unbounded; the run must succeed.
	out, stderr, code := run(t, env,
		"--src", "snap", "--unbounded", "--log.file=stderr", "--log.format=json", "keys")
	require.Zerof(t, code, "unbounded holistic query failed: %s", stderr)
	require.Contains(t, out, "alpha", "the keys must be emitted")

	// Sanity: without --unbounded the same filter is refused, so the flag is load-bearing.
	_, _, code = run(t, env, "--src", "snap", "keys")
	require.NotZero(t, code, "a holistic scan without --unbounded must be refused")
}

func TestSchemaEmitsJSONSchema(t *testing.T) {
	skipShort(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "data.jsonl")
	require.NoError(t, os.WriteFile(data,
		[]byte(`{"key":"1","value":{"name":"alice","age":30}}`+"\n"+`{"key":"2","value":{"name":"bob"}}`+"\n"), 0o600))
	env := []string{"IQ_CONFIG=" + filepath.Join(dir, "iq.toml")}

	out, stderr, code := run(t, env, "add", iqfile.URL(data), "-n", "snap")
	require.Zerof(t, code, "add failed: %s", stderr)
	require.Contains(t, out, "added source snap")

	out, stderr, code = run(t, env, "schema", "snap")
	require.Zerof(t, code, "schema failed: %s", stderr)

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	require.Equal(t, "https://json-schema.org/draft/2020-12/schema", doc["$schema"])
	require.Equal(t, "object", doc["type"])
	props := doc["properties"].(map[string]any)
	require.Equal(t, "string", props["name"].(map[string]any)["type"])
	require.Equal(t, "integer", props["age"].(map[string]any)["type"])
	// name is in both documents (required); age is in one (optional).
	require.Equal(t, []any{"name"}, doc["required"])
}

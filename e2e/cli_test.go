package e2e

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
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
	cmd := exec.CommandContext(t.Context(), iqBin, args...)
	cmd.Env = append(os.Environ(), env...)
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

func TestVersionReportsMetadata(t *testing.T) {
	skipShort(t)
	out, _, code := run(t, nil, "version")
	require.Zero(t, code)
	require.Contains(t, out, "commit:")
	require.Contains(t, out, "go:")
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

	out, stderr, code := run(t, env, "add", "file://"+data, "-n", "snap")
	require.Zerof(t, code, "add failed: %s", stderr)
	require.Contains(t, out, "added source snap")

	out, stderr, code = run(t, env, "--src", "snap", ".[]")
	require.Zerof(t, code, "query failed: %s", stderr)
	require.Contains(t, out, "hello-iq")
	require.Contains(t, out, "world-iq")
}

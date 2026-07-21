package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCompletionAllShells drives cobra's built-in completion generator for each
// supported shell: it must exit 0 and carry the shell's marker line.
func TestCompletionAllShells(t *testing.T) {
	skipShort(t)
	tests := []struct {
		shell, marker string
	}{
		{"bash", "# bash completion V2 for iq"},
		{"zsh", "#compdef iq"},
		{"fish", "# fish completion for iq"},
		{"powershell", "# powershell completion for iq"},
	}
	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			out, stderr, code := run(t, nil, "completion", tt.shell)
			require.Zerof(t, code, "completion %s failed: %s", tt.shell, stderr)
			require.Contains(t, out, tt.marker)
		})
	}
}

// TestManCommand renders the man page and checks its roff header.
func TestManCommand(t *testing.T) {
	skipShort(t)
	out, stderr, code := run(t, nil, "man")
	require.Zerof(t, code, "man failed: %s", stderr)
	require.Contains(t, out, ".TH IQ 1")
	require.Equal(t, byte('.'), out[0], "the page must start with the .TH request")
}

// TestDynamicCompletionListsHandle drives cobra's hidden __complete protocol with
// a seeded config: the saved source handle must appear as a candidate for --src
// and for a positional source argument, with the NoFileComp directive (:4).
func TestDynamicCompletionListsHandle(t *testing.T) {
	skipShort(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "iq.toml")
	require.NoError(t, os.WriteFile(cfg,
		[]byte("[sources.warehouse]\nurl = \"redis://localhost:6379/0\"\n"), 0o600))
	env := []string{"IQ_CONFIG=" + cfg}

	// Flag completion: `iq __complete --src ""`.
	out, stderr, code := run(t, env, "__complete", "--src", "")
	require.Zerof(t, code, "__complete --src failed: %s", stderr)
	require.Contains(t, out, "warehouse")
	require.Contains(t, out, ":4", "NoFileComp directive expected")

	// Positional completion: `iq inspect ""`.
	out, stderr, code = run(t, env, "__complete", "inspect", "")
	require.Zerof(t, code, "__complete inspect failed: %s", stderr)
	require.Contains(t, out, "warehouse")
}

// TestCompletionsMatchCommitted is the drift guard: the binary's completion
// output must equal the committed docs/completions/iq.<ext> files, so a cobra
// bump or help-text edit that changes them fails until `make completions` reruns.
func TestCompletionsMatchCommitted(t *testing.T) {
	skipShort(t)
	tests := []struct{ shell, ext string }{
		{"bash", "bash"},
		{"zsh", "zsh"},
		{"fish", "fish"},
		{"powershell", "ps1"},
	}
	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			out, stderr, code := run(t, nil, "completion", tt.shell)
			require.Zerof(t, code, "completion %s failed: %s", tt.shell, stderr)
			golden, err := os.ReadFile(filepath.Join("..", "docs", "completions", "iq."+tt.ext))
			require.NoError(t, err)
			require.Equalf(t, string(golden), out,
				"docs/completions/iq.%s is stale — run `make completions` and commit", tt.ext)
		})
	}
}

// TestManMatchesCommitted is the drift guard for the man page: the binary's `iq
// man` output must equal the committed docs/man/iq.1 byte-for-byte.
func TestManMatchesCommitted(t *testing.T) {
	skipShort(t)
	out, stderr, code := run(t, nil, "man")
	require.Zerof(t, code, "man failed: %s", stderr)
	golden, err := os.ReadFile(filepath.Join("..", "docs", "man", "iq.1"))
	require.NoError(t, err)
	require.Equal(t, string(golden), out,
		"docs/man/iq.1 is stale — run `make man` and commit")
}

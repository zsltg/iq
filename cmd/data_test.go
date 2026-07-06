package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

func TestDataFlagsValidate(t *testing.T) {
	require.NoError(t, (&dataFlags{}).validate())
	require.NoError(t, (&dataFlags{explain: true}).validate())
	require.NoError(t, (&dataFlags{dryRun: true}).validate())
	require.ErrorContains(t, (&dataFlags{explain: true, dryRun: true}).validate(), "mutually exclusive")
}

// dataTestConfig builds an in-memory config with one Redis and two Mongo sources
// for endpoint-resolution tests.
func dataTestConfig(t *testing.T) *iqconfig.Config {
	t.Helper()
	cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
	require.NoError(t, cf.Add("cache", "redis://h:6379/0", ""))
	require.NoError(t, cf.Add("books", "mongodb://h/db", "books"))
	return cf
}

func TestResolveEndpoint(t *testing.T) {
	cf := dataTestConfig(t)
	tests := []struct {
		name       string
		arg        string
		isDst      bool
		wantFile   bool
		wantPath   string
		wantDriver string
		wantColl   string
		wantErr    string
	}{
		{name: "redis source", arg: "cache", wantDriver: "redis"},
		{name: "mongo source with stored collection", arg: "books", wantDriver: "mongo", wantColl: "books"},
		{name: "mongo source with collection override", arg: "books.authors", wantDriver: "mongo", wantColl: "authors"},
		{name: "redis rejects collection suffix", arg: "cache.foo", wantErr: "no collections"},
		{name: "unknown name is not a file path", arg: "dump.jsonl", wantErr: "unknown source"},
		{name: "dash is stdin/stdout", arg: "-", wantFile: true, wantPath: "-"},
		{name: "empty dst is stdout", arg: "", isDst: true, wantFile: true, wantPath: ""},
		{name: "empty src is an error", arg: "", isDst: false, wantErr: "source is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep, err := resolveEndpoint(cf, tt.arg, tt.isDst)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantFile, ep.isFile)
			if tt.wantFile {
				require.Equal(t, tt.wantPath, ep.path)
				return
			}
			require.Equal(t, tt.wantDriver, ep.driver)
			require.Equal(t, tt.wantColl, ep.collection)
		})
	}
}

func TestEndpointLabel(t *testing.T) {
	require.Equal(t, "books.authors", endpoint{handle: "books", collection: "authors"}.label())
	require.Equal(t, "cache", endpoint{handle: "cache"}.label())
	require.Equal(t, "dump.jsonl", endpoint{isFile: true, path: "dump.jsonl"}.label())
	require.Equal(t, "stdout/stdin", endpoint{isFile: true, path: ""}.label())
}

func TestRunMoveRejectsConflictingFlags(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	// Flag validation happens before any connection, so these fail fast.
	err := runMove(cmd, &config{insert: "dst", noOverwrite: true, replace: true}, "")
	require.ErrorContains(t, err, "mutually exclusive")

	err = runMove(cmd, &config{typed: true, replace: true}, "")
	require.ErrorContains(t, err, "apply to --insert, not --typed")
}

func TestRenderMoveExplainTyped(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out strings.Builder
	cmd.SetOut(&out)
	require.NoError(t, runMove(cmd, &config{typed: true, explain: true, src: "cache"}, ""))
	require.Contains(t, out.String(), "move plan")
	require.Contains(t, out.String(), "typed dump")
}

func TestConfirmDestructionForce(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(""))
	require.NoError(t, confirmDestruction(cmd, "clear books", true))
}

package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
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
		{name: "unknown name is a file", arg: "dump.jsonl", wantFile: true, wantPath: "dump.jsonl"},
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

func TestRenderCopyPlanSourceToFile(t *testing.T) {
	src := endpoint{url: "redis://h:6379/0", driver: "redis", handle: "cache"}
	dst := endpoint{isFile: true, path: "dump.jsonl"}
	out := renderCopyPlan(src, dst, query.Upsert)
	require.Contains(t, out, "copy plan")
	require.Contains(t, out, "SCAN") // redis read describer
	require.Contains(t, out, "encode typed JSONL")
}

func TestRenderCopyPlanFileToSource(t *testing.T) {
	src := endpoint{isFile: true, path: "dump.jsonl"}
	dst := endpoint{url: "mongodb://h/db", driver: "mongo", handle: "books", collection: "books"}
	out := renderCopyPlan(src, dst, query.Upsert)
	require.Contains(t, out, "decode typed JSONL")
	require.Contains(t, out, "bulkWrite") // mongo write describer
}

func TestRunDataCopyRejectsConflictingFlags(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runDataCopy(cmd, &config{}, &dataFlags{explain: true, dryRun: true}, &copyOptions{}, []string{"a", "b"})
	require.ErrorContains(t, err, "mutually exclusive")

	err = runDataCopy(cmd, &config{}, &dataFlags{}, &copyOptions{noOverwrite: true, replace: true}, []string{"a", "b"})
	require.ErrorContains(t, err, "mutually exclusive")
}

func TestRunDataCopyRejectsFileToFile(t *testing.T) {
	configEnv(t) // empty config: both args are unknown, so both resolve to files.
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runDataCopy(cmd, &config{}, &dataFlags{}, &copyOptions{}, []string{"a.jsonl", "b.jsonl"})
	require.ErrorContains(t, err, "at least one endpoint must be a source")
}

func TestConfirmDestructionForce(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(""))
	require.NoError(t, confirmDestruction(cmd, "clear books", true))
}

package cmd

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// seedTwoGroups points config at a fresh temp file holding four sources across a
// single group (prod), so the offline completion helpers read a known set.
func seedTwoGroups(t *testing.T) {
	t.Helper()
	c := newSeed()
	require.NoError(t, c.Add("shop", "redis://localhost:6379/0"))
	require.NoError(t, c.Add("cache", "redis://localhost:6379/1"))
	require.NoError(t, c.Add("prod/books", "mongodb://localhost:27017/books"))
	require.NoError(t, c.Add("prod/users", "mongodb://localhost:27017/users"))
	c.Active = "shop"
	seedConfig(t, c)
}

func TestCompleteSourceHandles(t *testing.T) {
	seedTwoGroups(t)
	tests := []struct {
		name       string
		toComplete string
		want       []string
	}{
		{"all handles, sorted", "", []string{"cache", "prod/books", "prod/users", "shop"}},
		{"prefix filters", "sh", []string{"shop"}},
		{"group prefix", "prod/", []string{"prod/books", "prod/users"}},
		{"no match", "zzz", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, dir := completeSourceHandles(nil, nil, tt.toComplete)
			require.Equal(t, tt.want, got)
			require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
		})
	}
}

func TestCompleteGroups(t *testing.T) {
	seedTwoGroups(t)
	got, dir := completeGroups(nil, nil, "")
	require.Equal(t, []string{"prod"}, got)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)

	got, _ = completeGroups(nil, nil, "x")
	require.Empty(t, got)
}

func TestCompleteHandlesAndGroups(t *testing.T) {
	seedTwoGroups(t)
	got, dir := completeHandlesAndGroups(nil, nil, "")
	require.Equal(t, []string{"cache", "prod/books", "prod/users", "shop", "prod"}, got)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)

	// "prod" prefixes two handles and the group itself.
	got, _ = completeHandlesAndGroups(nil, nil, "prod")
	require.Equal(t, []string{"prod/books", "prod/users", "prod"}, got)
}

func TestCompleteConfigKeys(t *testing.T) {
	seedTwoGroups(t)
	got, dir := completeConfigKeys(nil, nil, "for")
	require.Equal(t, []string{"format", "format.decimal"}, got)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)

	// The full list is the persistableOptions allowlist (one source of truth).
	all, _ := completeConfigKeys(nil, nil, "")
	require.Equal(t, persistableOptions, all)
}

func TestCompleteCacheClearKeepsFileFallback(t *testing.T) {
	seedTwoGroups(t)
	got, dir := completeCacheClear(nil, nil, "ca")
	require.Equal(t, []string{"cache"}, got)
	// Default keeps the shell's path completion (cache clear also accepts a path).
	require.Equal(t, cobra.ShellCompDirectiveDefault, dir)
}

func TestFirstArgOnly(t *testing.T) {
	seedTwoGroups(t)
	fn := firstArgOnly(completeSourceHandles)

	// No args yet: delegates to the wrapped function.
	got, dir := fn(nil, nil, "")
	require.Equal(t, []string{"cache", "prod/books", "prod/users", "shop"}, got)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)

	// One arg present: offers nothing and no file completion.
	got, dir = fn(nil, []string{"shop"}, "")
	require.Nil(t, got)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
}

func TestCompleteEmptyConfig(t *testing.T) {
	// A missing config file loads as empty (no error): no candidates, no failure.
	t.Setenv("IQ_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	got, dir := completeSourceHandles(nil, nil, "")
	require.Empty(t, got)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)

	groups, _ := completeGroups(nil, nil, "")
	require.Empty(t, groups)
}

func TestCompleteConfigLoadError(t *testing.T) {
	// Point IQ_CONFIG at a directory so Load fails; completion yields no candidates
	// rather than erroring.
	t.Setenv("IQ_CONFIG", t.TempDir())
	got, dir := completeSourceHandles(nil, nil, "")
	require.Nil(t, got)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)

	// cache clear keeps its file fallback even on a load error.
	got, dir = completeCacheClear(nil, nil, "")
	require.Nil(t, got)
	require.Equal(t, cobra.ShellCompDirectiveDefault, dir)

	got, dir = completeHandlesAndGroups(nil, nil, "")
	require.Nil(t, got)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
}

// TestRootCompletionWiring pins that the root and key subcommands carry the
// completion wiring, so a refactor that drops a ValidArgsFunction is caught.
func TestRootCompletionWiring(t *testing.T) {
	root, _ := newRootCmd()
	require.NotNil(t, root.ValidArgsFunction, "root suppresses file completion for the jq filter")

	cases := map[string]bool{ // command path -> expects a ValidArgsFunction
		"add": true, "ls": true, "rm": true, "mv": true, "src": true,
		"group": true, "ping": true, "inspect": true, "schema": true, "diff": true,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			sub, _, err := root.Find([]string{name})
			require.NoError(t, err)
			if want {
				require.NotNilf(t, sub.ValidArgsFunction, "%s has no completion", name)
			}
		})
	}
}

package cmd

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqfile "github.com/zsltg/iq/drivers/file"
	"github.com/zsltg/iq/internal/numfmt"
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

// TestWithPrefixEmptyPreservesSlice pins that an empty toComplete returns the
// candidates slice unchanged — including a nil slice, which must stay nil rather
// than becoming a freshly allocated empty slice.
func TestWithPrefixEmptyPreservesSlice(t *testing.T) {
	require.Nil(t, withPrefix(nil, ""))
	require.Equal(t, []string{"a", "b"}, withPrefix([]string{"a", "b"}, ""))
	// A non-empty prefix still filters.
	require.Equal(t, []string{"a"}, withPrefix([]string{"a", "b"}, "a"))
	require.Empty(t, withPrefix([]string{"a", "b"}, "z"))
}

func TestFixedValues(t *testing.T) {
	fn := fixedValues("alpha", "beta", "gamma")

	got, dir := fn(nil, nil, "")
	require.Equal(t, []string{"alpha", "beta", "gamma"}, got)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)

	got, _ = fn(nil, nil, "b")
	require.Equal(t, []string{"beta"}, got)

	got, _ = fn(nil, nil, "z")
	require.Empty(t, got)
}

func TestCompleteCSV(t *testing.T) {
	const both = cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	fn := completeCSV(fixedValues("a", "b", "c"))
	tests := []struct {
		name       string
		toComplete string
		want       []string
		wantDir    cobra.ShellCompDirective
	}{
		{"empty offers all", "", []string{"a", "b", "c"}, both},
		{"bare prefix filters", "b", []string{"b"}, both},
		{"trailing comma re-attaches the head", "a,", []string{"a,b", "a,c"}, both},
		{"mid-word after a comma", "a,c", []string{"a,c"}, both},
		// A leading comma is the only input where the split boundary is
		// observable: the head is "," and every candidate keeps it.
		{"leading comma keeps an empty first segment", ",", []string{",a", ",b", ",c"}, both},
		{"leading comma with a prefix", ",b", []string{",b"}, both},
		{"several chosen", "a,b,", []string{"a,b,c"}, both},
		{"all chosen leaves nothing", "a,b,c,", nil, cobra.ShellCompDirectiveNoFileComp},
		{"no match leaves nothing", "z", nil, cobra.ShellCompDirectiveNoFileComp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, dir := fn(nil, nil, tt.toComplete)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantDir, dir)
		})
	}
}

func TestInspectSubcommands(t *testing.T) {
	tests := []struct {
		driver string
		want   []string
	}{
		{"mongo", mongoInspectCmds},
		{"redis", redisInfoCommonSections},
		{"cassandra", cassandraInspectCmds},
		{"dynamodb", dynamoInspectCmds},
		{"hbase", hbaseInspectCmds},
		{"couchdb", couchInspectCmds},
		{"couchbase", couchbaseInspectCmds},
		{"neo4j", neo4jInspectCmds},
		{"elasticsearch", elasticInspectCmds},
		{"opensearch", elasticInspectCmds},
		{"file", nil},
		{"", nil},
		{"nosuch", nil},
	}
	for _, tt := range tests {
		t.Run("driver "+tt.driver, func(t *testing.T) {
			require.Equal(t, tt.want, inspectSubcommands(tt.driver))
		})
	}
}

// seedMixedDrivers points config at a fresh temp file holding one source per
// driver family, so the driver-aware completions resolve a known backend.
func seedMixedDrivers(t *testing.T) {
	t.Helper()
	c := newSeed()
	require.NoError(t, c.Add("shop", "mongodb://localhost:27017/shop"))
	require.NoError(t, c.Add("cache", "redis://localhost:6379/0"))
	require.NoError(t, c.Add("cluster", "cassandra://localhost:9042/ks"))
	c.Active = "shop"
	seedConfig(t, c)
}

func TestSourceDriverName(t *testing.T) {
	seedMixedDrivers(t)
	root, _ := newRootCmd()

	t.Run("falls back to the active source", func(t *testing.T) {
		require.Equal(t, "mongo", sourceDriverName(root, nil))
	})

	t.Run("the positional wins over the active source", func(t *testing.T) {
		require.Equal(t, "cassandra", sourceDriverName(root, []string{"cluster"}))
	})

	t.Run("a dotted address resolves its base source", func(t *testing.T) {
		require.Equal(t, "mongo", sourceDriverName(root, []string{"shop.orders"}))
	})

	t.Run("--src wins over the active source", func(t *testing.T) {
		require.NoError(t, root.PersistentFlags().Set("src", "cache"))
		t.Cleanup(func() { require.NoError(t, root.PersistentFlags().Set("src", "")) })
		require.Equal(t, "redis", sourceDriverName(root, nil))
	})

	t.Run("an unknown source yields no driver", func(t *testing.T) {
		require.Empty(t, sourceDriverName(root, []string{"nosuch"}))
	})

	// Cobra always hands a completion its command, but the helper must not
	// dereference one it was not given: without the nil guard the --src lookup
	// panics here instead of falling through to the active source.
	t.Run("a nil command falls through to the active source", func(t *testing.T) {
		require.Equal(t, "mongo", sourceDriverName(nil, nil))
		require.Equal(t, "redis", sourceDriverName(nil, []string{"cache"}))
	})
}

// TestSourceDriverNameSkipsKeyring pins that the resolver reads the stored URL
// directly rather than through effectiveURL: a completion runs on a keypress, so
// a keyring read could block or prompt for an unlock. The keyring here holds no
// password, so a resolver that consulted it would fail and yield no driver.
func TestSourceDriverNameSkipsKeyring(t *testing.T) {
	fk := useFakeKeyring(t)
	c := newSeed()
	require.NoError(t, c.Add("locked", "mongodb://u@localhost:27017/db"))
	src := c.Sources["locked"]
	src.Keyring = true
	c.Sources["locked"] = src
	c.Active = "locked"
	seedConfig(t, c)

	root, _ := newRootCmd()
	require.Equal(t, "mongo", sourceDriverName(root, nil))
	require.Empty(t, fk.m, "completion must not have stored or read a keyring entry")
}

func TestCompleteInspectOnly(t *testing.T) {
	seedMixedDrivers(t)
	root, _ := newRootCmd()

	t.Run("the active source picks the mongo set", func(t *testing.T) {
		got, dir := completeInspectOnly(root, nil, "")
		require.Equal(t, mongoInspectCmds, got)
		require.Equal(t, cobra.ShellCompDirectiveNoFileComp|cobra.ShellCompDirectiveNoSpace, dir)
	})

	t.Run("a named source picks its own backend's set", func(t *testing.T) {
		got, _ := completeInspectOnly(root, []string{"cluster"}, "")
		require.Equal(t, cassandraInspectCmds, got)
	})

	t.Run("an unknown source yields nothing rather than erroring", func(t *testing.T) {
		got, dir := completeInspectOnly(root, []string{"nosuch"}, "")
		require.Nil(t, got)
		require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
	})
}

func TestCompleteConfigSet(t *testing.T) {
	seedMixedDrivers(t)
	root, _ := newRootCmd()
	c := findCmd(t, root, "config", "set")

	t.Run("first arg completes option names", func(t *testing.T) {
		got, dir := completeConfigSet(c, nil, "")
		require.Equal(t, persistableOptions, got)
		require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
	})

	t.Run("an enum option completes its values", func(t *testing.T) {
		got, _ := completeConfigSet(c, []string{"log.level"}, "")
		require.Equal(t, logLevelNames, got)
	})

	t.Run("a boolean option completes true/false", func(t *testing.T) {
		got, _ := completeConfigSet(c, []string{"verbose"}, "")
		require.Equal(t, []string{"true", "false"}, got)
	})

	t.Run("a free-form option completes nothing", func(t *testing.T) {
		got, dir := completeConfigSet(c, []string{"timeout"}, "")
		require.Empty(t, got)
		require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
	})

	t.Run("a third arg completes nothing", func(t *testing.T) {
		got, dir := completeConfigSet(c, []string{"format", "yaml"}, "")
		require.Nil(t, got)
		require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
	})

	t.Run("optionValues tolerates a nil command", func(t *testing.T) {
		require.Nil(t, optionValues(nil, "verbose"))
		// An enum option needs no command to resolve.
		require.Equal(t, logLevelNames, optionValues(nil, "log.level"))
	})

	// The option name comes from whatever the user has typed, which completion
	// never validates: `iq config set bogus <TAB>` reaches optionValues with a
	// name no flag carries, so the lookup returns nil and must not be
	// dereferenced for its type.
	t.Run("an unregistered option name completes nothing", func(t *testing.T) {
		require.Nil(t, optionValues(c, "not-a-flag"))
		got, dir := completeConfigSet(c, []string{"not-a-flag"}, "")
		require.Empty(t, got)
		require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
	})
}

// TestEnumAllowlistsMatchValidators feeds every completion candidate back
// through the validator it mirrors. The validators are switches, not slices, so
// this is what stops a candidate the CLI would reject from being offered.
func TestEnumAllowlistsMatchValidators(t *testing.T) {
	t.Run("format names round-trip", func(t *testing.T) {
		for _, name := range formatNames() {
			_, ok := namedFormat(name)
			require.Truef(t, ok, "--format %q is offered but not accepted", name)
		}
		require.Len(t, formatNames(), len(namedFormats), "formatNames must cover every named format")
	})

	t.Run("decimal modes round-trip", func(t *testing.T) {
		for _, name := range decimalModeNames {
			_, err := numfmt.ParseDecimalMode(name)
			require.NoErrorf(t, err, "--format.decimal %q is offered but not accepted", name)
		}
	})

	t.Run("log levels round-trip", func(t *testing.T) {
		for _, name := range logLevelNames {
			_, err := parseLogLevel(name)
			require.NoErrorf(t, err, "--log.level %q is offered but not accepted", name)
		}
	})

	t.Run("error formats round-trip", func(t *testing.T) {
		for _, name := range textJSONNames {
			require.NoErrorf(t, validateErrorFormat(name), "--error.format %q is offered but not accepted", name)
		}
	})

	t.Run("dump formats round-trip", func(t *testing.T) {
		for _, name := range dumpFormatNames {
			_, err := iqfile.ParseFormat(name)
			require.NoErrorf(t, err, "--from-format %q is offered but not accepted", name)
		}
	})

	t.Run("driver names round-trip", func(t *testing.T) {
		names := driverNameList()
		require.Len(t, names, len(drivers))
		for _, name := range names {
			_, ok := driverByName(name)
			require.Truef(t, ok, "--driver %q is offered but not in the registry", name)
		}
	})

	// --debug.pprof has no pure validator to round-trip against: startProfile
	// begins real profiling and writes a file. pprofModes is already the one
	// source of truth its switch and error message share, so completing from it
	// cannot drift; only its distinctness is worth pinning.
	t.Run("pprof modes are distinct", func(t *testing.T) {
		seen := map[string]bool{}
		for _, mode := range pprofModes {
			require.Falsef(t, seen[mode], "--debug.pprof offers %q twice", mode)
			seen[mode] = true
		}
		require.NotEmpty(t, pprofModes)
	})
}

// findCmd resolves a command by its full path from root, requiring an exact leaf.
func findCmd(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	c, _, err := root.Find(path)
	require.NoError(t, err)
	require.Equalf(t, path[len(path)-1], c.Name(), "path %v did not resolve to its leaf", path)
	return c
}

// TestCommandArgsAndCompletionWiring pins the Args validators and
// ValidArgsFunctions wired onto the leaf commands, so dropping either field (or
// perturbing an argument-count bound) is caught. Completion helpers are exercised
// against a seeded config so the wiring resolves to the right helper, not just a
// non-nil placeholder.
func TestCommandArgsAndCompletionWiring(t *testing.T) {
	seedTwoGroups(t)
	handles := []string{"cache", "prod/books", "prod/users", "shop"}
	root, _ := newRootCmd()

	t.Run("cache clear completes handles and keeps file fallback", func(t *testing.T) {
		c := findCmd(t, root, "cache", "clear")
		require.NotNil(t, c.ValidArgsFunction, "cache clear completion wiring dropped")
		got, dir := c.ValidArgsFunction(c, nil, "")
		require.Equal(t, handles, got)
		require.Equal(t, cobra.ShellCompDirectiveDefault, dir)
	})

	t.Run("config get completes keys and takes exactly one arg", func(t *testing.T) {
		c := findCmd(t, root, "config", "get")
		require.NotNil(t, c.ValidArgsFunction, "config get completion wiring dropped")
		got, dir := c.ValidArgsFunction(c, nil, "")
		require.Equal(t, persistableOptions, got)
		require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)

		require.NotNil(t, c.Args, "config get Args validator dropped")
		require.Error(t, c.Args(c, []string{}), "config get must reject zero args")
		require.NoError(t, c.Args(c, []string{"format"}))
		require.Error(t, c.Args(c, []string{"format", "yaml"}), "config get must reject two args")
	})

	t.Run("config set completes keys", func(t *testing.T) {
		c := findCmd(t, root, "config", "set")
		require.NotNil(t, c.ValidArgsFunction, "config set completion wiring dropped")
		got, dir := c.ValidArgsFunction(c, nil, "")
		require.Equal(t, persistableOptions, got)
		require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
	})

	// The lifecycle commands destroy data; without completion the shell offers
	// filenames there, so the wiring is pinned per command.
	for _, name := range []string{"clear", "drop"} {
		t.Run("data "+name+" completes every target", func(t *testing.T) {
			c := findCmd(t, root, "data", name)
			require.NotNilf(t, c.ValidArgsFunction, "data %s completion wiring dropped", name)
			got, dir := c.ValidArgsFunction(c, nil, "")
			require.Equal(t, handles, got)
			require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
			// A second target completes too — clear/drop take a list.
			got, _ = c.ValidArgsFunction(c, []string{"shop"}, "")
			require.Equal(t, handles, got)
		})
	}

	t.Run("data delete completes the target but not the keys", func(t *testing.T) {
		c := findCmd(t, root, "data", "delete")
		require.NotNil(t, c.ValidArgsFunction, "data delete completion wiring dropped")
		got, dir := c.ValidArgsFunction(c, nil, "")
		require.Equal(t, handles, got)
		require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)

		// The keys are opaque values: no candidates, and no filename fallback.
		got, dir = c.ValidArgsFunction(c, []string{"cache"}, "")
		require.Nil(t, got)
		require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
	})

	// combine takes one spec per source, so every positional completes handles —
	// not just the first — and the arg validator demands at least one.
	t.Run("combine completes handles for every spec", func(t *testing.T) {
		c := findCmd(t, root, "combine")
		require.NotNil(t, c.ValidArgsFunction, "combine completion wiring dropped")
		got, dir := c.ValidArgsFunction(c, nil, "")
		require.Equal(t, handles, got)
		require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)

		got, _ = c.ValidArgsFunction(c, []string{"shop=.[]"}, "")
		require.Equal(t, handles, got)

		require.Error(t, c.Args(c, nil), "combine needs at least one source")
		require.NoError(t, c.Args(c, []string{"shop=.[]"}))
	})

	t.Run("exec suppresses filename completion", func(t *testing.T) {
		c := findCmd(t, root, "exec")
		require.NotNil(t, c.ValidArgsFunction, "exec completion wiring dropped")
		got, dir := c.ValidArgsFunction(c, nil, "")
		require.Empty(t, got)
		require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
	})

	// The keyring subcommands all complete source handles for their first arg.
	for _, name := range []string{"get", "set", "rm", "migrate"} {
		t.Run("keyring "+name+" completes handles", func(t *testing.T) {
			c := findCmd(t, root, "config", "keyring", name)
			require.NotNilf(t, c.ValidArgsFunction, "keyring %s completion wiring dropped", name)
			got, dir := c.ValidArgsFunction(c, nil, "")
			require.Equal(t, handles, got)
			require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
		})
	}

	t.Run("keyring set takes one or two args", func(t *testing.T) {
		c := findCmd(t, root, "config", "keyring", "set")
		require.NotNil(t, c.Args, "keyring set Args validator dropped")
		require.Error(t, c.Args(c, []string{}), "keyring set must reject zero args")
		require.NoError(t, c.Args(c, []string{"shop"}))
		require.NoError(t, c.Args(c, []string{"shop", "secret"}))
		require.Error(t, c.Args(c, []string{"shop", "secret", "extra"}), "keyring set must reject three args")
	})
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

// TestFlagCompletionWiring pins the registered flag completions. Every one is
// registered with its error swallowed (it can only report an unknown flag), so a
// renamed flag would silently drop its completion without this.
func TestFlagCompletionWiring(t *testing.T) {
	seedMixedDrivers(t)
	root, _ := newRootCmd()

	t.Run("root flags", func(t *testing.T) {
		for _, flag := range []string{
			"src", "insert", "format", "from-format", "format.decimal",
			"log.level", "log.format", "error.format", "debug.pprof",
		} {
			_, ok := root.GetFlagCompletionFunc(flag)
			require.Truef(t, ok, "--%s has no completion", flag)
		}
	})

	t.Run("subcommand flags", func(t *testing.T) {
		cases := map[string][]string{
			"inspect": {"only"},
			"diff":    {"section"},
			"schema":  {"format"},
			"add":     {"driver", "store"},
		}
		for name, flags := range cases {
			c := findCmd(t, root, name)
			for _, flag := range flags {
				_, ok := c.GetFlagCompletionFunc(flag)
				require.Truef(t, ok, "%s --%s has no completion", name, flag)
			}
		}
	})

	t.Run("--driver offers the registry", func(t *testing.T) {
		c := findCmd(t, root, "add")
		fn, ok := c.GetFlagCompletionFunc("driver")
		require.True(t, ok)
		got, dir := fn(c, nil, "")
		require.Equal(t, driverNameList(), got)
		require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
	})

	t.Run("--insert offers saved sources", func(t *testing.T) {
		fn, ok := root.GetFlagCompletionFunc("insert")
		require.True(t, ok)
		got, _ := fn(root, nil, "")
		require.Equal(t, []string{"cache", "cluster", "shop"}, got)
	})
}

// TestCompleteGroupsLoadError checks that an unreadable config gives no group
// candidates and the no-file directive.
func TestCompleteGroupsLoadError(t *testing.T) {
	t.Setenv("IQ_CONFIG", t.TempDir())
	got, dir := completeGroups(nil, nil, "")
	require.Nil(t, got)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
}

// TestCompleteLoadErrorIgnoresThePrefix checks that an unreadable config gives
// nothing, with or without a typed prefix.
func TestCompleteLoadErrorIgnoresThePrefix(t *testing.T) {
	t.Setenv("IQ_CONFIG", t.TempDir())
	got, _ := completeSourceHandles(nil, nil, "sh")
	require.Nil(t, got)
}

// TestSourceDriverNameWithoutASource checks the empty answers: no active source,
// and an empty positional that falls through.
func TestSourceDriverNameWithoutASource(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://localhost:6379/0"))
	seedConfig(t, c)
	root, _ := newRootCmd()

	require.Empty(t, sourceDriverName(root, nil))
	require.Empty(t, sourceDriverName(root, []string{""}))

	require.NoError(t, root.PersistentFlags().Set("src", "cache"))
	t.Cleanup(func() { require.NoError(t, root.PersistentFlags().Set("src", "")) })
	require.Equal(t, "redis", sourceDriverName(root, []string{""}))
}

// TestCompleteCacheClearList checks the handle list and the file-fallback
// directive on success.
func TestCompleteCacheClearList(t *testing.T) {
	seedTwoGroups(t)
	got, dir := completeCacheClear(nil, nil, "")
	require.Equal(t, []string{"cache", "prod/books", "prod/users", "shop"}, got)
	require.Equal(t, cobra.ShellCompDirectiveDefault, dir)
}

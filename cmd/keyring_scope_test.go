package cmd

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/secret"
)

const (
	idA = "aaaaaaaaaaaaaaaa"
	idB = "bbbbbbbbbbbbbbbb"
)

// writeConfigFile saves c as a new config file and returns its path. It also
// points the config path at the new file.
func writeConfigFile(t *testing.T, c *iqconfig.Config) string {
	t.Helper()
	seedConfig(t, c)
	p, err := iqconfig.Path()
	require.NoError(t, err)
	return p
}

// useConfigFile points the config path at the file p.
func useConfigFile(t *testing.T, p string) { t.Helper(); t.Setenv(iqconfig.EnvConfig, p) }

// keyringSeed returns a config with the keyring sources that names lists.
func keyringSeed(t *testing.T, id string, names ...string) *iqconfig.Config {
	t.Helper()
	c := newSeed()
	c.KeyringID = id
	for _, n := range names {
		require.NoError(t, c.Add(n, "redis://u@h:6379/0"))
		require.NoError(t, c.UseKeyring(n))
	}
	return c
}

// TestAddInTwoConfigFilesDoesNotCollide adds the same handle in two config
// files. Both adds succeed and each file reads its own password.
func TestAddInTwoConfigFilesDoesNotCollide(t *testing.T) {
	fk := useFakeKeyring(t)
	pathA := writeConfigFile(t, newSeed())
	pathB := writeConfigFile(t, newSeed())
	files := []struct{ name, path, password string }{
		{"file A", pathA, "pw-a"},
		{"file B", pathB, "pw-b"},
	}
	for _, f := range files {
		t.Run("add in "+f.name, func(t *testing.T) {
			useConfigFile(t, f.path)
			_, err := runCmd(t, newAddCmd(&config{}), "-n", "sec", "redis://u:"+f.password+"@h:6379/0", "--store", "keyring", "--skip-verify")
			require.NoError(t, err)
		})
	}
	require.Len(t, fk.m, 2, "each file has its own entry")
	for _, f := range files {
		t.Run("read in "+f.name, func(t *testing.T) {
			useConfigFile(t, f.path)
			out, err := runCmd(t, newConfigKeyringCmd(&config{}), "get", "sec", "--reveal")
			require.NoError(t, err)
			require.Equal(t, f.password+"\n", out)
		})
	}
}

// TestMvDoesNotOverwriteTheEntryOfAnotherFile moves a source onto a handle that
// another config file uses. The entry of the other file keeps its password.
func TestMvDoesNotOverwriteTheEntryOfAnotherFile(t *testing.T) {
	fk := useFakeKeyring(t)
	pathA := writeConfigFile(t, keyringSeed(t, idA, "sec"))
	writeConfigFile(t, keyringSeed(t, idB, "prod/sec"))
	fk.m[idA+"/sec"] = "pw-a"
	fk.m[idB+"/prod/sec"] = "pw-b"
	useConfigFile(t, pathA)

	_, err := runCmd(t, newMvCmd(), "sec", "prod/sec")
	require.NoError(t, err)

	require.Equal(t, map[string]string{idA + "/prod/sec": "pw-a", idB + "/prod/sec": "pw-b"}, fk.m)
	require.Contains(t, fk.calls, "set "+idA+"/prod/sec")
	require.NotContains(t, fk.calls, "set prod/sec")
}

// TestPruneLeavesTheEntryOfAnotherFile prunes a stale entry in one config file.
// The entry of another file for the same handle stays.
func TestPruneLeavesTheEntryOfAnotherFile(t *testing.T) {
	fk := useFakeKeyring(t)
	a := keyringSeed(t, idA)
	require.NoError(t, a.Add("sec", "redis://h:6379/0")) // inline, so its entry is stale
	pathA := writeConfigFile(t, a)
	fk.m[idA+"/sec"] = "pw-a"
	fk.m[idB+"/sec"] = "pw-b"
	useConfigFile(t, pathA)

	_, err := runCmd(t, newConfigKeyringCmd(&config{}), "prune")
	require.NoError(t, err)

	require.Equal(t, map[string]string{idB + "/sec": "pw-b"}, fk.m)
	require.Equal(t, []string{idA + "/sec"}, fk.deleted)
}

// TestFileWithoutIDKeepsHandleAccounts reads in a config file that has no
// keyring ID. The accounts are the plain handles and the file is not written.
func TestFileWithoutIDKeepsHandleAccounts(t *testing.T) {
	tests := []struct {
		name string
		args func() *cobra.Command
		argv []string
	}{
		{"keyring get", func() *cobra.Command { return newConfigKeyringCmd(&config{}) }, []string{"get", "sec", "--reveal"}},
		{"keyring ls", func() *cobra.Command { return newConfigKeyringCmd(&config{}) }, []string{"ls"}},
		{"ls expand", func() *cobra.Command { return newLsCmd(&config{}) }, []string{"--expand"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fk := useFakeKeyring(t)
			p := writeConfigFile(t, keyringSeed(t, "", "sec"))
			fk.m["sec"] = "pw"
			before, err := os.ReadFile(p) //nolint:gosec // a temp file of the test.
			require.NoError(t, err)

			_, err = runCmd(t, tt.args(), tt.argv...)
			require.NoError(t, err)

			require.Equal(t, []string{"get sec"}, fk.calls)
			after, err := os.ReadFile(p) //nolint:gosec // a temp file of the test.
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "a read does not write the config")
		})
	}
}

// TestFirstKeyringWriteAssignsAnID runs each command that writes the keyring
// in a file that has no ID. The command saves a valid ID and copies the legacy
// entry of each keyring-backed source to its scoped account. It keeps the legacy
// entries.
func TestFirstKeyringWriteAssignsAnID(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T) error
		want func(id string) map[string]string
	}{
		{"add", func(t *testing.T) error {
			_, err := runCmd(t, newAddCmd(&config{}), "-n", "new", "redis://u:pw-new@h:6379/0", "--store", "keyring", "--skip-verify")
			return err
		}, func(id string) map[string]string {
			return map[string]string{"old": "pw-old", id + "/old": "pw-old", id + "/new": "pw-new"}
		}},
		{"keyring set", func(t *testing.T) error {
			_, err := runCmd(t, newConfigKeyringCmd(&config{}), "set", "old", "pw-set")
			return err
		}, func(id string) map[string]string {
			return map[string]string{"old": "pw-old", id + "/old": "pw-set"}
		}},
		{"keyring migrate", func(t *testing.T) error {
			_, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "inl")
			return err
		}, func(id string) map[string]string {
			return map[string]string{"old": "pw-old", id + "/old": "pw-old", id + "/inl": "pw-inl"}
		}},
		{"mv", func(t *testing.T) error {
			_, err := runCmd(t, newMvCmd(), "old", "moved")
			return err
		}, func(id string) map[string]string {
			return map[string]string{"old": "pw-old", id + "/moved": "pw-old"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fk := useFakeKeyring(t)
			c := keyringSeed(t, "", "old")
			require.NoError(t, c.Add("inl", "redis://u:pw-inl@h:6379/1"))
			writeConfigFile(t, c)
			fk.m["old"] = "pw-old"

			require.NoError(t, tt.run(t))

			cf, err := iqconfig.Load()
			require.NoError(t, err)
			require.True(t, iqconfig.ValidKeyringID(cf.KeyringID))
			require.Equal(t, tt.want(cf.KeyringID), fk.m)
		})
	}
}

// failingSet wraps a fake keyring so that the Set of one account fails.
type failingSet struct {
	*fakeKeyring
	suffix string
}

func (k failingSet) Set(account, password string) error {
	if strings.HasSuffix(account, k.suffix) {
		return errors.New("keyring locked")
	}
	return k.fakeKeyring.Set(account, password)
}

// TestEnsureKeyringIDRollsBack makes a copy fail and then a save fail. Each
// time the file gets no ID, the copies are deleted, and the legacy entries stay.
func TestEnsureKeyringIDRollsBack(t *testing.T) {
	t.Run("a failed copy", func(t *testing.T) {
		fk := useFakeKeyring(t)
		prev := keyringStore
		keyringStore = failingSet{fk, "/b"}
		t.Cleanup(func() { keyringStore = prev })
		c := keyringSeed(t, "", "a", "b")
		fk.m["a"], fk.m["b"] = "pw-a", "pw-b"

		rollback, err := ensureKeyringID(c)

		require.ErrorContains(t, err, "keyring locked")
		require.NotNil(t, rollback)
		require.Empty(t, c.KeyringID)
		require.Equal(t, map[string]string{"a": "pw-a", "b": "pw-b"}, fk.m)
		require.Len(t, fk.deleted, 2, "the copy of a and the failed copy of b are deleted")
		require.True(t, strings.HasSuffix(fk.deleted[0], "/a"))
	})
	t.Run("a failed read", func(t *testing.T) {
		fk := useFakeKeyring(t)
		c := keyringSeed(t, "", "a")
		fk.getErr = errors.New("keyring locked")

		_, err := ensureKeyringID(c)

		require.ErrorContains(t, err, "keyring locked")
		require.Empty(t, c.KeyringID)
		require.Empty(t, fk.m)
	})
	t.Run("a failed save", func(t *testing.T) {
		fk := useFakeKeyring(t)
		c := keyringSeed(t, "", "a")
		lockConfigDir(t, c)
		fk.m["a"] = "pw-a"

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "set", "a", "pw-new")

		require.ErrorContains(t, err, "create temp config")
		require.Equal(t, map[string]string{"a": "pw-a"}, fk.m, "the copies are gone and the legacy entry is untouched")
		cf, err := iqconfig.Load()
		require.NoError(t, err)
		require.Empty(t, cf.KeyringID)
	})
	t.Run("a failed save with no legacy entry leaves no scoped entry", func(t *testing.T) {
		fk := useFakeKeyring(t)
		c := keyringSeed(t, "", "a")
		lockConfigDir(t, c)

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "set", "a", "pw-new")

		require.ErrorContains(t, err, "create temp config")
		require.Empty(t, fk.m, "the new scoped entry is gone")
		cf, err := iqconfig.Load()
		require.NoError(t, err)
		require.Empty(t, cf.KeyringID)
	})
}

// TestReadFallsBackToTheLegacyEntry reads in a file that has an ID. A missing
// scoped entry falls back to the legacy entry. Any other error does not.
func TestReadFallsBackToTheLegacyEntry(t *testing.T) {
	tests := []struct {
		name      string
		entries   map[string]string
		getErr    error
		wantPW    string
		wantErr   string
		wantCalls []string
	}{
		{"scoped entry wins", map[string]string{idA + "/sec": "scoped", "sec": "legacy"}, nil, "scoped", "", []string{"get " + idA + "/sec"}},
		{"legacy entry fills a gap", map[string]string{"sec": "legacy"}, nil, "legacy", "", []string{"get " + idA + "/sec", "get sec"}},
		{"no entry", map[string]string{}, nil, "", "no keyring credential", []string{"get " + idA + "/sec", "get sec"}},
		{"another error stops", map[string]string{"sec": "legacy"}, errors.New("keyring locked"), "", "keyring locked", []string{"get " + idA + "/sec"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fk := useFakeKeyring(t)
			fk.m = tt.entries
			fk.getErr = tt.getErr
			cf := keyringSeed(t, idA, "sec")

			pw, err := keyringFor(cf).Get("sec")

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.wantPW, pw)
			require.Equal(t, tt.wantCalls, fk.calls)
		})
	}
}

// TestPruneLegacy drives `config keyring prune --legacy`.
func TestPruneLegacy(t *testing.T) {
	seed := func(t *testing.T, id string) *fakeKeyring {
		fk := useFakeKeyring(t)
		writeConfigFile(t, keyringSeed(t, id, "both", "legacy-only", "scoped-only"))
		fk.m["both"], fk.m[idA+"/both"] = "l1", "s1"
		fk.m["legacy-only"] = "l2"
		fk.m[idA+"/scoped-only"] = "s3"
		return fk
	}
	t.Run("force deletes only legacy entries that have a scoped copy", func(t *testing.T) {
		fk := seed(t, idA)
		out, err := runCmd(t, newConfigKeyringCmd(&config{}), "prune", "--legacy", "--force")
		require.NoError(t, err)
		require.Contains(t, out, "deleted legacy keyring entry for both")
		require.Equal(t, []string{"both"}, fk.deleted)
		require.Equal(t, map[string]string{idA + "/both": "s1", "legacy-only": "l2", idA + "/scoped-only": "s3"}, fk.m)
	})
	t.Run("dry run deletes nothing", func(t *testing.T) {
		fk := seed(t, idA)
		out, err := runCmd(t, newConfigKeyringCmd(&config{}), "prune", "--legacy", "--dry-run")
		require.NoError(t, err)
		require.Contains(t, out, "would delete legacy keyring entry for both")
		require.Empty(t, fk.deleted)
	})
	t.Run("a pipe without force asks for force", func(t *testing.T) {
		fk := seed(t, idA)
		stdinTerminal(t, false)
		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "prune", "--legacy")
		require.ErrorContains(t, err, "needs confirmation")
		require.Empty(t, fk.deleted)
	})
	t.Run("yes at the prompt deletes", func(t *testing.T) {
		fk := seed(t, idA)
		stdinTerminal(t, true)
		kc := newConfigKeyringCmd(&config{})
		kc.SetIn(strings.NewReader("y\n"))
		_, err := runCmd(t, kc, "prune", "--legacy")
		require.NoError(t, err)
		require.Equal(t, []string{"both"}, fk.deleted)
	})
	t.Run("no at the prompt keeps", func(t *testing.T) {
		fk := seed(t, idA)
		stdinTerminal(t, true)
		kc := newConfigKeyringCmd(&config{})
		kc.SetIn(strings.NewReader("n\n"))
		_, err := runCmd(t, kc, "prune", "--legacy")
		require.ErrorContains(t, err, "aborted")
		require.Empty(t, fk.deleted)
	})
	t.Run("a file without an ID is an error", func(t *testing.T) {
		fk := seed(t, "")
		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "prune", "--legacy", "--force")
		require.ErrorIs(t, err, errNoKeyringID)
		require.ErrorContains(t, err, "no keyring ID yet")
		require.Empty(t, fk.deleted)
	})
}

// TestRmDeletesOnlyTheScopedEntry removes a source in a file that has an ID.
// The legacy entry stays, because another file can use it.
func TestRmDeletesOnlyTheScopedEntry(t *testing.T) {
	fk := useFakeKeyring(t)
	writeConfigFile(t, keyringSeed(t, idA, "sec"))
	fk.m["sec"], fk.m[idA+"/sec"] = "legacy", "scoped"

	_, err := runCmd(t, newRmCmd(), "sec")
	require.NoError(t, err)

	require.Equal(t, map[string]string{"sec": "legacy"}, fk.m)
	require.Equal(t, []string{idA + "/sec"}, fk.deleted)
}

// stdinTerminal forces stdinIsTerminal to answer term for the test.
func stdinTerminal(t *testing.T, term bool) {
	t.Helper()
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return term }
	t.Cleanup(func() { stdinIsTerminal = orig })
}

// legacyGetFails wraps a fake keyring so that the Get of a legacy account (one
// with no "/") fails with a hard error.
type legacyGetFails struct{ *fakeKeyring }

func (k legacyGetFails) Get(account string) (string, error) {
	if !strings.Contains(account, "/") {
		return "", errors.New("keyring locked")
	}
	return k.fakeKeyring.Get(account)
}

// TestPruneLegacyFailures drives `config keyring prune --legacy` through each
// read, delete and write failure. Each one stops the command with its error.
func TestPruneLegacyFailures(t *testing.T) {
	tests := []struct {
		name    string
		store   func(fk *fakeKeyring) secret.Keyring
		seed    func(fk *fakeKeyring)
		args    []string
		outFail bool
		want    string
		deleted int
	}{
		{
			name:  "a read failure of the scoped entry",
			store: func(fk *fakeKeyring) secret.Keyring { fk.getErr = errors.New("keyring locked"); return fk },
			args:  []string{"prune", "--legacy", "--force"},
			want:  "keyring locked",
		},
		{
			name:  "a read failure of the legacy entry",
			store: func(fk *fakeKeyring) secret.Keyring { return legacyGetFails{fk} },
			seed:  func(fk *fakeKeyring) { fk.m[idA+"/both"] = "s1" },
			args:  []string{"prune", "--legacy", "--force"},
			want:  "keyring locked",
		},
		{
			name:  "no legacy entries",
			store: func(fk *fakeKeyring) secret.Keyring { return fk },
			seed:  func(fk *fakeKeyring) { fk.m[idA+"/both"] = "s1" },
			args:  []string{"prune", "--legacy", "--force"},
			want:  "",
		},
		{
			name:  "a delete failure",
			store: func(fk *fakeKeyring) secret.Keyring { return deleteFails{fk} },
			seed:  func(fk *fakeKeyring) { fk.m["both"], fk.m[idA+"/both"] = "l1", "s1" },
			args:  []string{"prune", "--legacy", "--force"},
			want:  "keyring locked",
		},
		{
			name:    "a write failure of the dry run report",
			store:   func(fk *fakeKeyring) secret.Keyring { return fk },
			seed:    func(fk *fakeKeyring) { fk.m["both"], fk.m[idA+"/both"] = "l1", "s1" },
			args:    []string{"prune", "--legacy", "--dry-run"},
			outFail: true,
			want:    "write failed",
		},
		{
			name:    "a write failure of the report after a delete",
			store:   func(fk *fakeKeyring) secret.Keyring { return fk },
			seed:    func(fk *fakeKeyring) { fk.m["both"], fk.m[idA+"/both"] = "l1", "s1" },
			args:    []string{"prune", "--legacy", "--force"},
			outFail: true,
			want:    "write failed",
			deleted: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fk := useFakeKeyring(t)
			writeConfigFile(t, keyringSeed(t, idA, "both"))
			if tt.seed != nil {
				tt.seed(fk)
			}
			keyringStore = tt.store(fk)
			kc := newConfigKeyringCmd(&config{})
			var out strings.Builder
			if tt.outFail {
				kc.SetOut(&errAfter{0})
			} else {
				kc.SetOut(&out)
			}
			kc.SetErr(&out)
			kc.SetArgs(tt.args)

			err := kc.Execute()

			if tt.want == "" {
				require.NoError(t, err)
				require.Contains(t, out.String(), "no legacy keyring entries")
			} else {
				require.ErrorContains(t, err, tt.want)
			}
			require.Len(t, fk.deleted, tt.deleted)
		})
	}
}

// TestMvKeyringID moves sources in a file that has no ID. A keyring-backed
// source gives the file an ID, and a failed copy stops the move. A plain source
// does not touch the keyring.
func TestMvKeyringID(t *testing.T) {
	t.Run("a failed copy stops the move", func(t *testing.T) {
		fk := useFakeKeyring(t)
		writeConfigFile(t, keyringSeed(t, "", "a"))
		fk.m["a"] = "pw-a"
		fk.getErr = errors.New("keyring locked")

		_, err := runCmd(t, newMvCmd(), "a", "b")

		require.ErrorContains(t, err, "keyring locked")
		cf, loadErr := iqconfig.Load()
		require.NoError(t, loadErr)
		require.Contains(t, cf.Sources, "a")
		require.NotContains(t, cf.Sources, "b")
		require.Empty(t, cf.KeyringID)
	})
	t.Run("a plain source leaves the keyring and the file ID alone", func(t *testing.T) {
		fk := useFakeKeyring(t)
		c := newSeed()
		require.NoError(t, c.Add("a", "redis://h:6379/0"))
		writeConfigFile(t, c)

		_, err := runCmd(t, newMvCmd(), "a", "b")

		require.NoError(t, err)
		cf, loadErr := iqconfig.Load()
		require.NoError(t, loadErr)
		require.Contains(t, cf.Sources, "b")
		require.Empty(t, cf.KeyringID)
		require.Empty(t, fk.calls)
	})
}

// TestMvKeyringFailures moves keyring sources and makes the keyring or the save
// fail. Each failure removes the new entries again.
func TestMvKeyringFailures(t *testing.T) {
	t.Run("a failed second copy deletes the first new entry", func(t *testing.T) {
		fk := useFakeKeyring(t)
		writeConfigFile(t, keyringSeed(t, idA, "g/a", "g/b"))
		fk.m[idA+"/g/a"], fk.m[idA+"/g/b"] = "pw-a", "pw-b"
		keyringStore = &secondSetFails{fakeKeyring: fk}

		_, err := runCmd(t, newMvCmd(), "g", "h")

		require.ErrorContains(t, err, "keyring locked")
		require.Equal(t, map[string]string{idA + "/g/a": "pw-a", idA + "/g/b": "pw-b"}, fk.m)
	})
	t.Run("a failed save deletes the new entry and the copies of a new ID", func(t *testing.T) {
		fk := useFakeKeyring(t)
		c := keyringSeed(t, "", "a", "c")
		lockConfigDir(t, c)
		fk.m["a"], fk.m["c"] = "pw-a", "pw-c"

		_, err := runCmd(t, newMvCmd(), "a", "b")

		require.ErrorContains(t, err, "create temp config")
		require.Equal(t, map[string]string{"a": "pw-a", "c": "pw-c"}, fk.m, "only the legacy entries are left")
		cf, loadErr := iqconfig.Load()
		require.NoError(t, loadErr)
		require.Empty(t, cf.KeyringID)
		require.Contains(t, cf.Sources, "a")
	})
	t.Run("a failed save of a plain source returns the error", func(t *testing.T) {
		useFakeKeyring(t)
		c := newSeed()
		require.NoError(t, c.Add("a", "redis://h:6379/0"))
		lockConfigDir(t, c)

		_, err := runCmd(t, newMvCmd(), "a", "b")

		require.ErrorContains(t, err, "create temp config")
	})
}

// TestInspectHeaderUsesScopedKeyring expands the location of a source in a
// config file with a keyring ID. Only the scoped entry holds the password.
func TestInspectHeaderUsesScopedKeyring(t *testing.T) {
	fk := useFakeKeyring(t)
	writeConfigFile(t, keyringSeed(t, idA, "sec"))
	fk.m[idA+"/sec"] = "pw-a"
	cfg := &config{expand: true, reveal: true}

	require.NoError(t, resolveInspectSource(cfg, "sec"))
	var buf bytes.Buffer
	require.NoError(t, inspectHeader(&buf, cfg))

	require.Contains(t, buf.String(), "redis://u:pw-a@h:6379/0")
}

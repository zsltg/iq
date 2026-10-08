package cmd

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

func TestKeyringStatusCell(t *testing.T) {
	// Present is green, missing is red — swapping them (a negated condition) must
	// change the rendered color, so the two cells never share a palette entry.
	require.Equal(t, pal.add, keyringStatusCell("present").c)
	require.Equal(t, pal.remove, keyringStatusCell("missing").c)
	require.NotEqual(t, keyringStatusCell("present").c, keyringStatusCell("missing").c)
}

// seedKeyringSource registers a source, optionally marks it keyring-backed, and
// (when pw is non-empty) stores its secret in the fake keyring.
func seedKeyringSource(t *testing.T, c *iqconfig.Config, fk *fakeKeyring, handle, url string, keyring bool, pw string) {
	t.Helper()
	require.NoError(t, c.Add(handle, url))
	if keyring {
		require.NoError(t, c.UseKeyring(handle))
	}
	if pw != "" {
		require.NoError(t, fk.Set(iqconfig.CleanHandle(handle), pw))
	}
}

func TestConfigKeyringLs(t *testing.T) {
	configEnv(t)
	c := newSeed()
	fk := useFakeKeyring(t)
	seedKeyringSource(t, c, fk, "present", "redis://u@h:6379/0", true, "secret")
	seedKeyringSource(t, c, fk, "absent", "redis://u@h:6379/1", true, "") // keyring flag but no entry
	seedKeyringSource(t, c, fk, "plain", "redis://h:6379/2", false, "")   // not keyring-backed
	seedConfig(t, c)

	out, err := runCmd(t, newConfigKeyringCmd(&config{}), "ls")
	require.NoError(t, err)
	require.Contains(t, out, "present")
	require.Contains(t, out, "absent")
	require.Contains(t, out, "missing")
	require.NotContains(t, out, "plain") // non-keyring sources are excluded
}

func TestConfigKeyringLsEmpty(t *testing.T) {
	configEnv(t)
	c := newSeed()
	fk := useFakeKeyring(t)
	seedKeyringSource(t, c, fk, "plain", "redis://h:6379/0", false, "")
	seedConfig(t, c)

	out, err := runCmd(t, newConfigKeyringCmd(&config{}), "ls")
	require.NoError(t, err)
	require.Contains(t, out, "no keyring-backed sources")
}

func TestConfigKeyringLsJSON(t *testing.T) {
	configEnv(t)
	c := newSeed()
	fk := useFakeKeyring(t)
	seedKeyringSource(t, c, fk, "sec", "redis://u@h:6379/0", true, "secret")
	seedConfig(t, c)

	out, err := runCmd(t, newConfigKeyringCmd(&config{}), "ls", "-j")
	require.NoError(t, err)
	var rows []keyringRow
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Equal(t, []keyringRow{{Handle: "sec", Status: "present"}}, rows)

	// -j and -y are mutually exclusive.
	_, err = runCmd(t, newConfigKeyringCmd(&config{}), "ls", "-j", "-y")
	require.Error(t, err)
}

func TestConfigKeyringGet(t *testing.T) {
	configEnv(t)
	c := newSeed()
	fk := useFakeKeyring(t)
	seedKeyringSource(t, c, fk, "sec", "redis://u@h:6379/0", true, "secret")
	seedKeyringSource(t, c, fk, "plain", "redis://h:6379/1", false, "")
	seedConfig(t, c)

	// Redacted by default.
	out, err := runCmd(t, newConfigKeyringCmd(&config{}), "get", "sec")
	require.NoError(t, err)
	require.NotContains(t, out, "secret")
	require.Contains(t, out, "xxxxx")

	// --reveal prints the secret verbatim.
	out, err = runCmd(t, newConfigKeyringCmd(&config{}), "get", "sec", "--reveal")
	require.NoError(t, err)
	require.Contains(t, out, "secret")

	// A non-keyring source has no secret to get.
	_, err = runCmd(t, newConfigKeyringCmd(&config{}), "get", "plain")
	require.ErrorContains(t, err, "not keyring-backed")

	// Unknown source.
	_, err = runCmd(t, newConfigKeyringCmd(&config{}), "get", "ghost")
	require.ErrorContains(t, err, "unknown source")
}

func TestConfigKeyringSet(t *testing.T) {
	t.Run("updates an existing keyring secret", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "sec", "redis://u@h:6379/0", true, "old")
		seedConfig(t, c)

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "set", "sec", "new")
		require.NoError(t, err)
		require.Equal(t, "new", fk.m["sec"])
	})

	t.Run("marks a password-less source keyring-backed", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "sec", "redis://h:6379/0", false, "")
		seedConfig(t, c)

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "set", "sec", "pw")
		require.NoError(t, err)
		require.Equal(t, "pw", fk.m["sec"])
		cf, err := iqconfig.Load()
		require.NoError(t, err)
		require.True(t, cf.Sources["sec"].Keyring)
	})

	t.Run("reads the value from stdin when omitted", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "sec", "redis://h:6379/0", false, "")
		seedConfig(t, c)

		kc := newConfigKeyringCmd(&config{})
		kc.SetIn(strings.NewReader("frompipe\n"))
		_, err := runCmd(t, kc, "set", "sec")
		require.NoError(t, err)
		require.Equal(t, "frompipe", fk.m["sec"])
	})

	t.Run("rejects a source with an inline password", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "sec", "redis://u:inline@h:6379/0", false, "")
		seedConfig(t, c)

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "set", "sec", "pw")
		require.ErrorContains(t, err, "inline password")
		require.Empty(t, fk.m["sec"])
	})
}

func TestConfigKeyringRm(t *testing.T) {
	configEnv(t)
	c := newSeed()
	fk := useFakeKeyring(t)
	seedKeyringSource(t, c, fk, "sec", "redis://u@h:6379/0", true, "secret")
	seedConfig(t, c)

	out, err := runCmd(t, newConfigKeyringCmd(&config{}), "rm", "sec")
	require.NoError(t, err)
	require.Contains(t, out, "deleted keyring secret")
	_, ok := fk.m["sec"]
	require.False(t, ok)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.False(t, cf.Sources["sec"].Keyring)

	// A non-keyring source has nothing to remove.
	_, err = runCmd(t, newConfigKeyringCmd(&config{}), "rm", "sec")
	require.ErrorContains(t, err, "not keyring-backed")
}

func TestConfigKeyringMigrate(t *testing.T) {
	t.Run("moves an inline password into the keyring", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "sec", "redis://u:inline@h:6379/0", false, "")
		seedConfig(t, c)

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "sec")
		require.NoError(t, err)
		require.Equal(t, "inline", fk.m["sec"])
		cf, err := iqconfig.Load()
		require.NoError(t, err)
		require.True(t, cf.Sources["sec"].Keyring)
		require.Equal(t, "redis://u@h:6379/0", cf.Sources["sec"].URL)
	})

	t.Run("--all migrates every inline source with a password", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "a", "redis://u:pa@h:6379/0", false, "")
		seedKeyringSource(t, c, fk, "b", "redis://u:pb@h:6379/1", false, "")
		seedKeyringSource(t, c, fk, "nopw", "redis://h:6379/2", false, "")
		seedConfig(t, c)

		out, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "--all")
		require.NoError(t, err)
		require.Equal(t, "pa", fk.m["a"])
		require.Equal(t, "pb", fk.m["b"])
		_, ok := fk.m["nopw"]
		require.False(t, ok)
		// Every migrated source is reported (a short-circuited loop would drop one).
		require.Contains(t, out, "migrated a")
		require.Contains(t, out, "migrated b")
	})

	t.Run("--all --dry-run reports every target", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "a", "redis://u:pa@h:6379/0", false, "")
		seedKeyringSource(t, c, fk, "b", "redis://u:pb@h:6379/1", false, "")
		seedConfig(t, c)

		out, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "--all", "--dry-run")
		require.NoError(t, err)
		require.Contains(t, out, "would migrate a")
		require.Contains(t, out, "would migrate b")
		require.Empty(t, fk.m)
	})

	t.Run("--dry-run writes nothing", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "sec", "redis://u:inline@h:6379/0", false, "")
		seedConfig(t, c)

		out, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "sec", "--dry-run")
		require.NoError(t, err)
		require.Contains(t, out, "would migrate sec")
		require.Empty(t, fk.m)
		cf, err := iqconfig.Load()
		require.NoError(t, err)
		require.False(t, cf.Sources["sec"].Keyring)
	})

	t.Run("rejects --all with two handles and migrates nothing", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "a", "redis://u:pa@h:6379/0", false, "")
		seedKeyringSource(t, c, fk, "b", "redis://u:pb@h:6379/1", false, "")
		seedConfig(t, c)

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "--all", "a", "b")

		require.EqualError(t, err, "accepts at most 1 arg(s), received 2")
		require.Empty(t, fk.m)
		cf, err := iqconfig.Load()
		require.NoError(t, err)
		require.False(t, cf.Sources["a"].Keyring)
		require.False(t, cf.Sources["b"].Keyring)
	})

	t.Run("rejects both a handle and --all", func(t *testing.T) {
		configEnv(t)
		seedConfig(t, newSeed())
		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "sec", "--all")
		require.ErrorContains(t, err, "not both or neither")
	})

	t.Run("rejects neither a handle nor --all", func(t *testing.T) {
		configEnv(t)
		seedConfig(t, newSeed())
		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate")
		require.ErrorContains(t, err, "not both or neither")
	})

	t.Run("rejects an already keyring-backed source", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "sec", "redis://u@h:6379/0", true, "secret")
		seedConfig(t, c)

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "sec")
		require.ErrorContains(t, err, "already keyring-backed")
	})
}

func TestConfigKeyringPrune(t *testing.T) {
	configEnv(t)
	c := newSeed()
	fk := useFakeKeyring(t)
	// A non-keyring source that nonetheless has a stray keyring entry (e.g. left
	// by a crash between Set and Save) is the one prunable case.
	seedKeyringSource(t, c, fk, "stale", "redis://h:6379/0", false, "orphan")
	seedKeyringSource(t, c, fk, "live", "redis://u@h:6379/1", true, "secret")
	seedConfig(t, c)

	// --dry-run reports but keeps the entry.
	out, err := runCmd(t, newConfigKeyringCmd(&config{}), "prune", "--dry-run")
	require.NoError(t, err)
	require.Contains(t, out, "would delete stale keyring entry for stale")
	require.Contains(t, out, "cannot be detected") // the enumeration-limit note
	require.Equal(t, "orphan", fk.m["stale"])

	// A real prune deletes it, leaving the keyring-backed source's secret alone.
	out, err = runCmd(t, newConfigKeyringCmd(&config{}), "prune")
	require.NoError(t, err)
	require.Contains(t, out, "deleted stale keyring entry for stale")
	require.NotContains(t, out, "no stale keyring entries") // something was pruned
	_, ok := fk.m["stale"]
	require.False(t, ok)
	require.Equal(t, "secret", fk.m["live"])

	// Nothing left to prune: the "none" line and the enumeration-limit note both show.
	out, err = runCmd(t, newConfigKeyringCmd(&config{}), "prune")
	require.NoError(t, err)
	require.Contains(t, out, "no stale keyring entries")
	require.Contains(t, out, "cannot be detected")
}

// TestConfigKeyringMigrateKeepsOtherEntries drives a migration against a
// keyring that holds an entry for the handle, or that cannot be read. The
// migration stops before its first keyring write and does not replace the entry.
func TestConfigKeyringMigrateKeepsOtherEntries(t *testing.T) {
	t.Run("keeps an existing keyring entry and writes nothing", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "a", "redis://u:pa@h:6379/0", false, "")
		seedKeyringSource(t, c, fk, "b", "redis://u:pb@h:6379/1", false, "")
		seedConfig(t, c)
		fk.m["b"] = "other"

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "--all")

		require.EqualError(t, err, "b: the OS keyring already holds a password for this handle, and a source in another config file can use it; rename the source with iq mv, then migrate it")
		require.ErrorIs(t, err, errKeyringTaken)
		require.Equal(t, map[string]string{"b": "other"}, fk.m, "a is not written and b keeps its entry")
		require.Empty(t, fk.deleted, "nothing to roll back")
		cf, err := iqconfig.Load()
		require.NoError(t, err)
		require.Equal(t, "redis://u:pa@h:6379/0", cf.Sources["a"].URL)
		require.False(t, cf.Sources["a"].Keyring)
	})

	t.Run("a URI that does not parse stops the migration", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "sec", "redis://u:p%zz@h:6379/0", false, "")
		seedConfig(t, c)

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "sec")

		require.ErrorIs(t, err, errInvalidURI)
		require.Empty(t, fk.m)
	})

	t.Run("a keyring write failure saves nothing", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "sec", "redis://u:inline@h:6379/0", false, "")
		seedConfig(t, c)
		fk.setErr = errors.New("keyring locked")

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "sec")

		require.EqualError(t, err, "keyring locked")
		cf, err := iqconfig.Load()
		require.NoError(t, err)
		require.Equal(t, "redis://u:inline@h:6379/0", cf.Sources["sec"].URL)
		require.False(t, cf.Sources["sec"].Keyring)
	})

	t.Run("a keyring read failure stops the migration", func(t *testing.T) {
		configEnv(t)
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "sec", "redis://u:inline@h:6379/0", false, "")
		seedConfig(t, c)
		fk.getErr = errors.New("keyring locked")

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "sec")

		require.EqualError(t, err, "keyring locked")
		require.Empty(t, fk.m)
		cf, err := iqconfig.Load()
		require.NoError(t, err)
		require.Equal(t, "redis://u:inline@h:6379/0", cf.Sources["sec"].URL)
	})
}

// secondSetFails wraps a fake keyring so that its second Set call fails, which
// leaves the first secret written when a multi-source migrate stops.
type secondSetFails struct {
	*fakeKeyring
	sets int
}

func (k *secondSetFails) Set(handle, password string) error {
	k.sets++
	if k.sets == 2 {
		return errors.New("keyring locked")
	}
	return k.fakeKeyring.Set(handle, password)
}

// deleteFails wraps a fake keyring so every Delete fails.
type deleteFails struct{ *fakeKeyring }

func (deleteFails) Delete(string) error { return errors.New("keyring locked") }

// TestConfigKeyringArgCounts pins the argument bounds of each keyring command.
func TestConfigKeyringArgCounts(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "ls takes none", args: []string{"ls", "extra"}},
		{name: "get needs one", args: []string{"get"}},
		{name: "get takes one", args: []string{"get", "a", "b"}},
		{name: "set needs one", args: []string{"set"}},
		{name: "set takes two", args: []string{"set", "a", "b", "c"}},
		{name: "rm needs one", args: []string{"rm"}},
		{name: "rm takes one", args: []string{"rm", "a", "b"}},
		{name: "migrate takes one", args: []string{"migrate", "a", "b"}},
		{name: "prune takes none", args: []string{"prune", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newSeed()
			fk := useFakeKeyring(t)
			seedKeyringSource(t, c, fk, "a", "redis://u@h:6379/0", true, "secret")
			seedConfig(t, c)
			_, err := runCmd(t, newConfigKeyringCmd(&config{}), tt.args...)
			require.Error(t, err)
		})
	}
}

// TestConfigKeyringCorruptConfig checks each command stops on a config it cannot
// parse, before it touches the keyring.
func TestConfigKeyringCorruptConfig(t *testing.T) {
	for _, args := range [][]string{
		{"ls"}, {"get", "a"}, {"set", "a", "v"}, {"rm", "a"}, {"migrate", "a"}, {"prune"},
	} {
		t.Run(args[0], func(t *testing.T) {
			p := configEnv(t)
			require.NoError(t, os.WriteFile(p, []byte("not = [valid toml\n"), 0o600))
			fk := useFakeKeyring(t)
			_, err := runCmd(t, newConfigKeyringCmd(&config{}), args...)
			require.ErrorContains(t, err, "parse config")
			require.Empty(t, fk.calls, "no keyring call before the config parses")
		})
	}
}

// TestConfigKeyringOutputFailures fails the write of each report line and expects
// the error back.
func TestConfigKeyringOutputFailures(t *testing.T) {
	seed := func(t *testing.T) {
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "kr", "redis://u@h:6379/0", true, "secret")
		seedKeyringSource(t, c, fk, "inline", "redis://u:pw@h:6379/1", false, "")
		seedKeyringSource(t, c, fk, "plain", "redis://h:6379/2", false, "")
		seedKeyringSource(t, c, fk, "stale", "redis://h:6379/3", false, "old")
		seedConfig(t, c)
	}
	tests := []struct {
		name   string
		args   []string
		stderr bool
	}{
		{name: "ls table", args: []string{"ls"}},
		{name: "ls json", args: []string{"ls", "-j"}},
		{name: "ls yaml", args: []string{"ls", "-y"}},
		{name: "get", args: []string{"get", "kr"}},
		{name: "set", args: []string{"set", "kr", "v"}},
		{name: "rm", args: []string{"rm", "kr"}},
		{name: "migrate", args: []string{"migrate", "inline"}},
		{name: "migrate dry run", args: []string{"migrate", "inline", "--dry-run"}},
		{name: "prune", args: []string{"prune"}},
		{name: "prune dry run", args: []string{"prune", "--dry-run"}},
		{name: "prune note", args: []string{"prune"}, stderr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seed(t)
			c := newConfigKeyringCmd(&config{})
			if tt.stderr {
				c.SetOut(io.Discard)
				c.SetErr(&errAfter{0})
			} else {
				c.SetOut(&errAfter{0})
				c.SetErr(io.Discard)
			}
			c.SetArgs(tt.args)
			require.ErrorContains(t, c.Execute(), "write failed")
		})
	}
}

// TestConfigKeyringPruneNoStale checks the empty report and its write error.
func TestConfigKeyringPruneNoStale(t *testing.T) {
	seed := func(t *testing.T) {
		t.Helper()
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "plain", "redis://h:6379/0", false, "")
		seedConfig(t, c)
	}

	t.Run("reports no stale entries", func(t *testing.T) {
		seed(t)
		out, err := runCmd(t, newConfigKeyringCmd(&config{}), "prune")
		require.NoError(t, err)
		require.Contains(t, out, "no stale keyring entries")
	})

	t.Run("returns the report write error", func(t *testing.T) {
		seed(t)
		cmd := newConfigKeyringCmd(&config{})
		cmd.SetOut(&errAfter{0})
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"prune"})
		require.ErrorContains(t, cmd.Execute(), "write failed")
	})
}

// TestConfigKeyringPruneSkipsMissingEntries checks a source with no entry does not
// stop the scan: a stale entry on a later source is still pruned.
func TestConfigKeyringPruneSkipsMissingEntries(t *testing.T) {
	c := newSeed()
	fk := useFakeKeyring(t)
	seedKeyringSource(t, c, fk, "a-clean", "redis://h:6379/0", false, "")
	seedKeyringSource(t, c, fk, "b-stale", "redis://h:6379/1", false, "old")
	seedConfig(t, c)

	out, err := runCmd(t, newConfigKeyringCmd(&config{}), "prune")
	require.NoError(t, err)
	require.Contains(t, out, "deleted stale keyring entry for b-stale")
	require.NotContains(t, fk.m, "b-stale")
}

// TestConfigKeyringPruneReportsADeleteFailure checks the delete error comes back
// and no success line is printed for the entry.
func TestConfigKeyringPruneReportsADeleteFailure(t *testing.T) {
	c := newSeed()
	fk := newFakeKeyring()
	prev := keyringStore
	keyringStore = deleteFails{fk}
	t.Cleanup(func() { keyringStore = prev })
	seedKeyringSource(t, c, fk, "stale", "redis://h:6379/0", false, "old")
	seedConfig(t, c)

	out, err := runCmd(t, newConfigKeyringCmd(&config{}), "prune")
	require.ErrorContains(t, err, "keyring locked")
	require.NotContains(t, out, "deleted stale")
}

// TestConfigKeyringGetRefusals checks the unknown and non-keyring refusals.
func TestConfigKeyringGetRefusals(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		wantErr string
	}{
		{name: "a source without a keyring entry", source: "plain", wantErr: "is not keyring-backed"},
		{name: "an unknown source", source: "nosuch", wantErr: "unknown source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newSeed()
			fk := useFakeKeyring(t)
			seedKeyringSource(t, c, fk, "plain", "redis://h:6379/0", false, "")
			seedConfig(t, c)

			_, err := runCmd(t, newConfigKeyringCmd(&config{}), "get", tt.source)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestConfigKeyringLsYAML checks -y alone selects the structured form.
func TestConfigKeyringLsYAML(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantOut string
		wantErr string
	}{
		{name: "-y alone prints YAML", args: []string{"ls", "-y"}, wantOut: "status: present"},
		{name: "-y with -j is refused", args: []string{"ls", "-j", "-y"}, wantErr: "none of the others can be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newSeed()
			fk := useFakeKeyring(t)
			seedKeyringSource(t, c, fk, "kr", "redis://u@h:6379/0", true, "secret")
			seedConfig(t, c)

			out, err := runCmd(t, newConfigKeyringCmd(&config{}), tt.args...)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Contains(t, out, tt.wantOut)
		})
	}
}

// TestConfigKeyringSetFailures covers the keyring write and config save failures
// of `set` and the save failure of `rm`.
func TestConfigKeyringSetFailures(t *testing.T) {
	t.Run("a keyring write failure changes nothing", func(t *testing.T) {
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "plain", "redis://h:6379/0", false, "")
		seedConfig(t, c)
		fk.setErr = errors.New("keyring locked")

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "set", "plain", "v")
		require.ErrorContains(t, err, "keyring locked")
		saved, loadErr := iqconfig.Load()
		require.NoError(t, loadErr)
		require.False(t, saved.Sources["plain"].Keyring)
	})

	t.Run("a save failure removes the new entry again", func(t *testing.T) {
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "plain", "redis://h:6379/0", false, "")
		lockConfigDir(t, c)

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "set", "plain", "v")
		require.ErrorContains(t, err, "create temp config")
		require.NotContains(t, fk.m, "plain")
	})

	t.Run("rm keeps the secret when the save fails", func(t *testing.T) {
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "kr", "redis://u@h:6379/0", true, "secret")
		lockConfigDir(t, c)

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "rm", "kr")
		require.ErrorContains(t, err, "create temp config")
		require.Equal(t, "secret", fk.m["kr"])
	})
}

// TestConfigKeyringMigrateEdges covers the empty run, the scan past a keyring
// source, the save failure and the partial write rollback.
func TestConfigKeyringMigrateEdges(t *testing.T) {
	t.Run("all with nothing inline reports nothing to migrate", func(t *testing.T) {
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "plain", "redis://h:6379/0", false, "")
		seedConfig(t, c)

		out, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "--all")
		require.NoError(t, err)
		require.Contains(t, out, "nothing to migrate")
	})

	t.Run("all goes past a keyring source to later inline ones", func(t *testing.T) {
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "a-kr", "redis://u@h:6379/0", true, "secret")
		seedKeyringSource(t, c, fk, "b-inline", "redis://u:pw@h:6379/1", false, "")
		seedConfig(t, c)

		out, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "--all")
		require.NoError(t, err)
		require.Contains(t, out, "migrated b-inline to the keyring")
		require.Equal(t, "pw", fk.m["b-inline"])
	})

	t.Run("a save failure rolls the secrets back", func(t *testing.T) {
		c := newSeed()
		fk := useFakeKeyring(t)
		seedKeyringSource(t, c, fk, "inline", "redis://u:pw@h:6379/1", false, "")
		lockConfigDir(t, c)

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "inline")
		require.ErrorContains(t, err, "create temp config")
		require.NotContains(t, fk.m, "inline")
	})

	t.Run("a failed second write removes the first secret", func(t *testing.T) {
		c := newSeed()
		fk := newFakeKeyring()
		prev := keyringStore
		keyringStore = &secondSetFails{fakeKeyring: fk}
		t.Cleanup(func() { keyringStore = prev })
		seedKeyringSource(t, c, fk, "a-inline", "redis://u:pw@h:6379/0", false, "")
		seedKeyringSource(t, c, fk, "b-inline", "redis://u:pw@h:6379/1", false, "")
		seedConfig(t, c)

		_, err := runCmd(t, newConfigKeyringCmd(&config{}), "migrate", "--all")
		require.ErrorContains(t, err, "keyring locked")
		require.Empty(t, fk.m, "no secret is left behind")
		saved, loadErr := iqconfig.Load()
		require.NoError(t, loadErr)
		require.False(t, saved.Sources["a-inline"].Keyring)
	})
}

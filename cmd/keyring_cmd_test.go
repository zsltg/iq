package cmd

import (
	"encoding/json"
	"errors"
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

	t.Run("keeps an existing keyring entry and rolls back", func(t *testing.T) {
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
		require.Equal(t, map[string]string{"b": "other"}, fk.m, "a is rolled back and b keeps its entry")
		cf, err := iqconfig.Load()
		require.NoError(t, err)
		require.Equal(t, "redis://u:pa@h:6379/0", cf.Sources["a"].URL)
		require.False(t, cf.Sources["a"].Keyring)
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

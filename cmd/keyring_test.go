package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/secret"
)

// fakeKeyring is an in-memory Keyring for tests; it never touches the OS store.
// setErr, when set, makes every Set fail, and getErr every Get, which is how a
// test drives the keyring failure paths of a command. deleted records each
// Delete call. calls records every Get, Set and Delete as "op handle", so a test
// can assert that a command left the keyring alone.
type fakeKeyring struct {
	m       map[string]string
	setErr  error
	getErr  error
	deleted []string
	calls   []string
}

func newFakeKeyring() *fakeKeyring { return &fakeKeyring{m: map[string]string{}} }

func (f *fakeKeyring) Set(handle, password string) error {
	f.calls = append(f.calls, "set "+handle)
	if f.setErr != nil {
		return f.setErr
	}
	f.m[handle] = password
	return nil
}

func (f *fakeKeyring) Get(handle string) (string, error) {
	f.calls = append(f.calls, "get "+handle)
	if f.getErr != nil {
		return "", f.getErr
	}
	pw, ok := f.m[handle]
	if !ok {
		return "", fmt.Errorf("%w for %q", secret.ErrNotFound, handle)
	}
	return pw, nil
}

func (f *fakeKeyring) Delete(handle string) error {
	f.calls = append(f.calls, "delete "+handle)
	f.deleted = append(f.deleted, handle)
	delete(f.m, handle)
	return nil
}

// useFakeKeyring swaps in an in-memory keyring for the duration of the test and
// restores the real store afterwards.
func useFakeKeyring(t *testing.T) *fakeKeyring {
	t.Helper()
	prev := keyringStore
	fk := newFakeKeyring()
	keyringStore = fk
	t.Cleanup(func() { keyringStore = prev })
	return fk
}

func TestSplitPassword(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		wantStripped string
		wantPass     string
		wantOK       bool
	}{
		{"user and password", "redis://u:secret@h:6379/0", "redis://u@h:6379/0", "secret", true},
		{"password only drops empty user", "redis://:secret@h:6379/0", "redis://h:6379/0", "secret", true},
		{"multi-host mongo", "mongodb://u:p@h1,h2/db?replicaSet=rs", "mongodb://u@h1,h2/db?replicaSet=rs", "p", true},
		{"user only, no password", "redis://u@h:6379", "redis://u@h:6379", "", false},
		{"no userinfo", "mongodb://h:27017/db", "mongodb://h:27017/db", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripped, pass, ok, err := splitPassword(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantStripped, stripped)
			require.Equal(t, tt.wantPass, pass)
		})
	}
}

// TestParseURIHidesTheURI parses a URI that net/url rejects. The error wraps
// errInvalidURI and quotes no part of the URI, because the part can be a
// password.
func TestParseURIHidesTheURI(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bad percent escape", "redis://u:p%zzsecret@h", "parse URI: the URI is not valid: it has a percent sign that does not start a valid escape (write a literal % as %25)"},
		{"bad port", "redis://u:secret@h:port/0", "parse URI: the URI is not valid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, splitErr := splitPassword(tt.in)
			_, injectErr := injectPassword(tt.in, "x")

			for _, err := range []error{splitErr, injectErr} {
				require.EqualError(t, err, tt.want)
				require.ErrorIs(t, err, errInvalidURI)
			}
		})
	}
}

func TestInjectPassword(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"keeps username", "redis://u@h:6379/0", "redis://u:NEWPW@h:6379/0"},
		{"no username", "redis://h:6379/0", "redis://:NEWPW@h:6379/0"},
		{"multi-host mongo", "mongodb://u@h1,h2/db", "mongodb://u:NEWPW@h1,h2/db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := injectPassword(tt.in, "NEWPW")
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSplitThenInjectRoundTrips(t *testing.T) {
	const raw = "mongodb://u:s3cr3t@h1,h2/db?replicaSet=rs"
	stripped, pass, ok, err := splitPassword(raw)
	require.NoError(t, err)
	require.True(t, ok)
	got, err := injectPassword(stripped, pass)
	require.NoError(t, err)
	require.Equal(t, raw, got)
}

func TestEffectiveURL(t *testing.T) {
	t.Run("non-keyring returns stored url", func(t *testing.T) {
		useFakeKeyring(t)
		u, err := effectiveURL(iqconfig.Source{URL: "redis://h:6379/0"}, "cache")
		require.NoError(t, err)
		require.Equal(t, "redis://h:6379/0", u)
	})

	t.Run("keyring splices password back in", func(t *testing.T) {
		fk := useFakeKeyring(t)
		require.NoError(t, fk.Set("sec", "secret"))
		u, err := effectiveURL(iqconfig.Source{URL: "redis://u@h:6379/0", Keyring: true}, "sec")
		require.NoError(t, err)
		require.Equal(t, "redis://u:secret@h:6379/0", u)
	})

	t.Run("keyring miss errors", func(t *testing.T) {
		useFakeKeyring(t)
		_, err := effectiveURL(iqconfig.Source{URL: "redis://u@h:6379/0", Keyring: true}, "gone")
		require.ErrorIs(t, err, secret.ErrNotFound)
	})
}

func TestAddStoreKeyring(t *testing.T) {
	seedConfig(t, newSeed())
	fk := useFakeKeyring(t)

	out, err := runCmd(t, newAddCmd(&config{}), "-n", "sec", "redis://u:secret@h:6379/0", "--store", "keyring", "--skip-verify")
	require.NoError(t, err)
	require.Contains(t, out, "added source sec")

	cf, err := iqconfig.Load()
	require.NoError(t, err)
	s, ok := cf.Sources["sec"]
	require.True(t, ok)
	require.True(t, s.Keyring)
	require.Equal(t, "redis://u@h:6379/0", s.URL)
	require.NotContains(t, s.URL, "secret")
	require.Equal(t, "secret", fk.m["sec"])
}

// fallbackWarning is the warning `iq add` prints when the password of source
// sec goes to the config file because the keyring refused it.
const fallbackWarning = "warning: could not write the password of sec to the OS keyring, so it is in the config file\n" +
	"to move it later, unlock or set up the keyring and run: iq config keyring migrate sec\n" +
	"to keep a password in the config file without this warning, add the source with --store inline\n"

// TestAddDefaultStore drives `iq add` without --store: the password goes to the
// keyring, a URI with no password stays as it is, and a keyring that cannot be
// read or written leaves the password in the config file with one warning.
func TestAddDefaultStore(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		getErr      error
		setErr      error
		wantURL     string
		wantKeyring bool
		wantStored  string
		wantWarning string
	}{
		{"password goes to the keyring", "redis://u:secret@h:6379/0", nil, nil, "redis://u@h:6379/0", true, "secret", ""},
		{"no password stays inline", "redis://u@h:6379/0", nil, nil, "redis://u@h:6379/0", false, "", ""},
		{"refused write falls back to inline", "redis://u:secret@h:6379/0", nil, errors.New("no secret service"), "redis://u:secret@h:6379/0", false, "", fallbackWarning},
		{"refused read falls back to inline", "redis://u:secret@h:6379/0", errors.New("keyring locked"), nil, "redis://u:secret@h:6379/0", false, "", fallbackWarning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedConfig(t, newSeed())
			fk := useFakeKeyring(t)
			fk.getErr, fk.setErr = tt.getErr, tt.setErr
			c := newAddCmd(&config{})
			var stdout, stderr bytes.Buffer
			c.SetOut(&stdout)
			c.SetErr(&stderr)
			c.SetArgs([]string{"-n", "sec", tt.url, "--skip-verify"})

			require.NoError(t, c.Execute())

			require.Equal(t, "added source sec\n", stdout.String())
			require.Equal(t, tt.wantWarning, stderr.String())
			cf, err := iqconfig.Load()
			require.NoError(t, err)
			require.Equal(t, tt.wantURL, cf.Sources["sec"].URL)
			require.Equal(t, tt.wantKeyring, cf.Sources["sec"].Keyring)
			require.Equal(t, tt.wantStored, fk.m["sec"])
			require.Empty(t, fk.deleted)
		})
	}
}

// TestAddKeepsAnExistingKeyringEntry adds a source whose handle already has a
// keyring entry, which can belong to a source in another config file. The add
// fails, with or without --store, and the entry keeps its password.
func TestAddKeepsAnExistingKeyringEntry(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"default store", nil},
		{"explicit keyring", []string{"--store", "keyring"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedConfig(t, newSeed())
			fk := useFakeKeyring(t)
			fk.m["sec"] = "first"
			args := append([]string{"-n", "sec", "redis://u:second@h:6379/0", "--skip-verify"}, tt.args...)

			_, err := runCmd(t, newAddCmd(&config{}), args...)

			require.EqualError(t, err, "sec: the OS keyring already holds a password for this handle, and a source in another config file can use it; choose another handle with -n")
			require.ErrorIs(t, err, errKeyringTaken)
			require.Equal(t, "first", fk.m["sec"])
			cf, err := iqconfig.Load()
			require.NoError(t, err)
			require.Empty(t, cf.Sources)
		})
	}
}

// TestAddExplicitKeyringReadFailure makes the keyring read fail under an
// explicit --store keyring: the add fails and saves nothing.
func TestAddExplicitKeyringReadFailure(t *testing.T) {
	seedConfig(t, newSeed())
	fk := useFakeKeyring(t)
	fk.getErr = errors.New("keyring locked")

	_, err := runCmd(t, newAddCmd(&config{}), "-n", "sec", "redis://u:secret@h:6379/0", "--store", "keyring", "--skip-verify")

	require.ErrorContains(t, err, "keyring locked")
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.Empty(t, cf.Sources)
}

// TestAddStoreInline keeps the password in the config file and never writes to
// the keyring.
func TestAddStoreInline(t *testing.T) {
	seedConfig(t, newSeed())
	fk := useFakeKeyring(t)

	out, err := runCmd(t, newAddCmd(&config{}), "-n", "sec", "redis://u:secret@h:6379/0", "--store", "inline", "--skip-verify")
	require.NoError(t, err)
	require.Equal(t, "added source sec\n", out)

	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.Equal(t, "redis://u:secret@h:6379/0", cf.Sources["sec"].URL)
	require.False(t, cf.Sources["sec"].Keyring)
	require.Empty(t, fk.m)
}

// TestAddSaveFailureKeyringCleanup makes the config save fail at the end of an
// add. A password that iq wrote to the keyring is deleted again, so the failed
// add leaves no trace. After a fallback, iq wrote nothing to the keyring, so it
// does not call the keyring again, and it prints no warning that the password
// is in the config file.
func TestAddSaveFailureKeyringCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not stop writes on Windows, so this failure cannot be forced there")
	}
	tests := []struct {
		name        string
		setErr      error
		wantDeleted []string
	}{
		{"written password is deleted", nil, []string{"sec"}},
		{"fallback leaves the keyring alone", errors.New("no secret service"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(iqconfig.EnvConfig, filepath.Join(dir, "iq.toml"))
			require.NoError(t, newSeed().Save())
			require.NoError(t, os.Chmod(dir, 0o500)) // readable and listable, but not writable.
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			fk := useFakeKeyring(t)
			fk.setErr = tt.setErr

			out, err := runCmd(t, newAddCmd(&config{}), "-n", "sec", "redis://u:secret@h:6379/0", "--skip-verify")

			require.ErrorContains(t, err, "create temp config")
			require.NotContains(t, out, "warning")
			require.Equal(t, tt.wantDeleted, fk.deleted)
			require.Empty(t, fk.m)
		})
	}
}

// TestAddRejectsAnUnparsableURI returns the parse error for every store, so no
// source is saved that cannot connect.
func TestAddRejectsAnUnparsableURI(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"default store", nil},
		{"explicit keyring", []string{"--store", "keyring"}},
		{"inline", []string{"--store", "inline"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedConfig(t, newSeed())
			fk := useFakeKeyring(t)
			args := append([]string{"-n", "sec", "redis://u:p%zz@h:6379/0", "--skip-verify"}, tt.args...)

			_, err := runCmd(t, newAddCmd(&config{}), args...)

			require.ErrorIs(t, err, errInvalidURI)
			cf, err := iqconfig.Load()
			require.NoError(t, err)
			require.Empty(t, cf.Sources)
			require.Empty(t, fk.m)
		})
	}
}

func TestAddStoreKeyringNoPassword(t *testing.T) {
	seedConfig(t, newSeed())
	useFakeKeyring(t)

	_, err := runCmd(t, newAddCmd(&config{}), "-n", "sec", "redis://u@h:6379/0", "--store", "keyring", "--skip-verify")
	require.ErrorContains(t, err, "no password")

	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.Empty(t, cf.Sources)
}

func TestAddStoreInvalid(t *testing.T) {
	seedConfig(t, newSeed())
	_, err := runCmd(t, newAddCmd(&config{}), "-n", "sec", "redis://u:p@h", "--store", "vault", "--skip-verify")
	require.ErrorContains(t, err, "unknown --store")
}

func TestMvMigratesKeyringEntry(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("sec", "redis://u@h:6379/0"))
	require.NoError(t, c.UseKeyring("sec"))
	seedConfig(t, c)
	fk := useFakeKeyring(t)
	require.NoError(t, fk.Set("sec", "secret"))

	_, err := runCmd(t, newMvCmd(), "sec", "prod/sec")
	require.NoError(t, err)

	_, oldExists := fk.m["sec"]
	require.False(t, oldExists, "old keyring entry should be gone")
	require.Equal(t, "secret", fk.m["prod/sec"])
}

func TestLsRevealKeyringSource(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("sec", "redis://u@h:6379/0"))
	require.NoError(t, c.UseKeyring("sec"))
	seedConfig(t, c)
	fk := useFakeKeyring(t)
	require.NoError(t, fk.Set("sec", "secret"))

	// Default listing shows the stored (password-less) URL, not the secret.
	plain, err := runCmd(t, newLsCmd(&config{}))
	require.NoError(t, err)
	require.NotContains(t, plain, "secret")

	// --reveal alone un-redacts inline passwords only; a keyring password is not
	// resolved, so the secret still never appears.
	revealed, err := runCmd(t, newLsCmd(&config{}), "--reveal")
	require.NoError(t, err)
	require.NotContains(t, revealed, "secret")

	// --expand resolves the keyring password but, without --reveal, redacts it.
	expanded, err := runCmd(t, newLsCmd(&config{}), "--expand")
	require.NoError(t, err)
	require.NotContains(t, expanded, "secret")
	require.Contains(t, expanded, "redis://u:xxxxx@h:6379/0")

	// --reveal --expand resolves the keyring password and prints it verbatim.
	both, err := runCmd(t, newLsCmd(&config{}), "--reveal", "--expand")
	require.NoError(t, err)
	require.Contains(t, both, "redis://u:secret@h:6379/0")
}

func TestRmDeletesKeyringEntry(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("sec", "redis://u@h:6379/0"))
	require.NoError(t, c.UseKeyring("sec"))
	seedConfig(t, c)
	fk := useFakeKeyring(t)
	require.NoError(t, fk.Set("sec", "secret"))

	_, err := runCmd(t, newRmCmd(), "sec")
	require.NoError(t, err)
	_, ok := fk.m["sec"]
	require.False(t, ok)
}

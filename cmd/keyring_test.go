package cmd

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/secret"
)

// fakeKeyring is an in-memory Keyring for tests; it never touches the OS store.
// setErr, when set, makes every Set fail, which is how a test drives the
// keyring-write failure path of a command.
type fakeKeyring struct {
	m      map[string]string
	setErr error
}

func newFakeKeyring() *fakeKeyring { return &fakeKeyring{m: map[string]string{}} }

func (f *fakeKeyring) Set(handle, password string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.m[handle] = password
	return nil
}

func (f *fakeKeyring) Get(handle string) (string, error) {
	pw, ok := f.m[handle]
	if !ok {
		return "", fmt.Errorf("%w for %q", secret.ErrNotFound, handle)
	}
	return pw, nil
}

func (f *fakeKeyring) Delete(handle string) error {
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

func TestSplitPasswordRejectsUnparseable(t *testing.T) {
	_, _, _, err := splitPassword("redis://u:%zz@h")
	require.ErrorContains(t, err, "parse URI")
	// The cause stays unwrappable (%w, not %v), so a caller can errors.As to
	// the parse failure underneath the context.
	var uerr *url.Error
	require.ErrorAs(t, err, &uerr)
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

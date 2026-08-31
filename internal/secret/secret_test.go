package secret_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/zsltg/iq/internal/secret"
)

func TestOSKeyringRoundTrip(t *testing.T) {
	keyring.MockInit() // in-memory provider; never touches the real OS store.
	k := secret.OSKeyring{}

	require.NoError(t, k.Set("cache", "secret"))

	got, err := k.Get("cache")
	require.NoError(t, err)
	require.Equal(t, "secret", got)

	require.NoError(t, k.Delete("cache"))

	_, err = k.Get("cache")
	require.ErrorIs(t, err, secret.ErrNotFound)

	// Deleting a missing entry is idempotent, not an error.
	require.NoError(t, k.Delete("cache"))
}

func TestOSKeyringSetReplaces(t *testing.T) {
	keyring.MockInit()
	k := secret.OSKeyring{}

	require.NoError(t, k.Set("cache", "old"))
	require.NoError(t, k.Set("cache", "new"))

	got, err := k.Get("cache")
	require.NoError(t, err)
	require.Equal(t, "new", got)
}

func TestOSKeyringSurfacesTheProviderError(t *testing.T) {
	errProvider := errors.New("keyring unavailable")
	tests := []struct {
		name    string
		call    func(secret.Keyring) error
		wantMsg string
	}{
		{
			name:    "set",
			call:    func(k secret.Keyring) error { return k.Set("cache", "secret") },
			wantMsg: `store keyring credential for "cache"`,
		},
		{
			name: "get",
			call: func(k secret.Keyring) error {
				_, err := k.Get("cache")
				return err
			},
			wantMsg: `read keyring credential for "cache"`,
		},
		{
			name:    "delete",
			call:    func(k secret.Keyring) error { return k.Delete("cache") },
			wantMsg: `delete keyring credential for "cache"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A provider failure that is not ErrNotFound: every operation must
			// report it, named by handle and with the cause still unwrappable.
			keyring.MockInitWithError(errProvider)

			err := tt.call(secret.OSKeyring{})

			require.ErrorContains(t, err, tt.wantMsg)
			require.ErrorIs(t, err, errProvider)
			require.NotErrorIs(t, err, secret.ErrNotFound)
		})
	}
}

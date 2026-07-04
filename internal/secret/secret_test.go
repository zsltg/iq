package secret_test

import (
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

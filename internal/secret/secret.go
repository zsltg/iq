// Package secret stores a source's password in the OS keyring, keeping the
// plaintext out of the config file. The config records only that a source is
// keyring-backed; the password is fetched here at connect time and spliced back
// into the URL by the composition root. Keyring is a port so tests can inject an
// in-memory fake and never touch the real OS secret store.
package secret

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// service is the keyring service name every iq credential is stored under; the
// per-source account is the source's full handle.
const service = "iq"

// ErrNotFound is returned by Get when no credential is stored for the handle.
var ErrNotFound = errors.New("no keyring credential")

// Keyring stores, retrieves, and deletes a source password keyed by its handle.
type Keyring interface {
	Set(handle, password string) error
	Get(handle string) (string, error)
	Delete(handle string) error
}

// OSKeyring is the production Keyring backed by the operating system's secret
// store (Secret Service/gnome-keyring on Linux, Keychain on macOS, Credential
// Manager on Windows).
type OSKeyring struct{}

// Set stores password for handle, replacing any existing entry.
func (OSKeyring) Set(handle, password string) error {
	if err := keyring.Set(service, handle, password); err != nil {
		return fmt.Errorf("store keyring credential for %q: %w", handle, err)
	}
	return nil
}

// Get returns the stored password for handle, or ErrNotFound if none exists.
func (OSKeyring) Get(handle string) (string, error) {
	pw, err := keyring.Get(service, handle)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", fmt.Errorf("%w for %q", ErrNotFound, handle)
	}
	if err != nil {
		return "", fmt.Errorf("read keyring credential for %q: %w", handle, err)
	}
	return pw, nil
}

// Delete removes the stored password for handle. A missing entry is not an
// error, so a best-effort cleanup is idempotent.
func (OSKeyring) Delete(handle string) error {
	err := keyring.Delete(service, handle)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete keyring credential for %q: %w", handle, err)
	}
	return nil
}

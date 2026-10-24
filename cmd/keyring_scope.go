package cmd

import (
	"errors"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/secret"
)

// fileKeyring is the keyring of one config file. It maps the handle of a source
// to a keyring account, so that two config files that share a handle do not
// share an entry. When the file has a keyring ID, the account is
// "<keyring_id>/<handle>". When it has none, the account is the handle alone.
// That is the account of an iq version that had no ID, the legacy account.
// It reads cf.KeyringID at each call, so a new ID takes effect at once. It
// implements secret.Keyring. When exact is set, Get never falls back to the
// legacy account.
type fileKeyring struct {
	cf    *iqconfig.Config
	store secret.Keyring
	exact bool
}

// keyringFor returns the keyring of the config file cf.
func keyringFor(cf *iqconfig.Config) *fileKeyring {
	return &fileKeyring{cf: cf, store: keyringStore}
}

// account returns the keyring account of handle in this file.
func (k *fileKeyring) account(handle string) string {
	if k.cf.KeyringID == "" {
		return handle
	}
	return k.cf.KeyringID + "/" + handle
}

// Set stores the password under the account of this file.
func (k *fileKeyring) Set(handle, password string) error {
	return k.store.Set(k.account(handle), password)
}

// Get reads the account of this file. If the file has an ID, exact is not set,
// and that account has no entry, it reads the legacy account. That covers a
// copy that stopped part way. Only a missing entry falls back. Any other error
// is returned.
func (k *fileKeyring) Get(handle string) (string, error) {
	pw, err := k.store.Get(k.account(handle))
	if !k.fallsBack(err) {
		return pw, err
	}
	return k.store.Get(handle)
}

// fallsBack reports whether a read that ended with err reads the legacy account
// next. Only a missing entry falls back, and never in an exact view. In a file
// without an ID the account is the legacy account, so the read repeats itself.
func (k *fileKeyring) fallsBack(err error) bool {
	if k.exact {
		return false
	}
	return errors.Is(err, secret.ErrNotFound)
}

// exactView returns a view of the same store that never falls back to the legacy
// account.
func (k *fileKeyring) exactView() *fileKeyring {
	return &fileKeyring{cf: k.cf, store: k.store, exact: true}
}

// legacyView returns a view of the same store whose account is the handle alone,
// the legacy account.
func (k *fileKeyring) legacyView() *fileKeyring {
	return &fileKeyring{cf: &iqconfig.Config{}, store: k.store}
}

// Delete removes the account of this file. It never removes the legacy account.
func (k *fileKeyring) Delete(handle string) error {
	return k.store.Delete(k.account(handle))
}

// ensureKeyringID gives the config file cf a keyring ID when it has none. It
// generates the ID, then copies the legacy entry of every keyring-backed source
// of the file to its scoped account. It does not delete a legacy entry, because
// another config file can still use it. It does not save cf: the command saves
// cf after its own write, so the ID and that write reach the file together.
// The returned function undoes the assignment. It deletes the copies and clears
// the ID. The command calls it when a later step or the save fails. If a copy
// fails, ensureKeyringID undoes its own work and returns the error. A file that
// has an ID needs no work, and its rollback does nothing.
func ensureKeyringID(cf *iqconfig.Config) (rollback func(), err error) {
	noop := func() {}
	if cf.KeyringID != "" {
		return noop, nil
	}
	id := iqconfig.NewKeyringID()
	scoped := &fileKeyring{cf: &iqconfig.Config{KeyringID: id}, store: keyringStore}
	copied, err := copyLegacyEntries(cf, scoped)
	undo := func() {
		for _, h := range copied {
			_ = scoped.Delete(h)
		}
		cf.KeyringID = ""
	}
	if err != nil {
		undo()
		return noop, err
	}
	cf.KeyringID = id
	return undo, nil
}

// copyLegacyEntries copies the legacy entry of every keyring-backed source of
// cf to its account in scoped. It returns the handles that it wrote, also when
// it fails, so that the caller can delete them.
func copyLegacyEntries(cf *iqconfig.Config, scoped *fileKeyring) (copied []string, err error) {
	for _, h := range cf.List() {
		if !h.Source.Keyring {
			continue
		}
		clean := iqconfig.CleanHandle(h.Name)
		pw, err := keyringStore.Get(clean)
		if errors.Is(err, secret.ErrNotFound) {
			continue
		}
		if err != nil {
			return copied, err
		}
		copied = append(copied, clean) // a failed Set can leave an entry
		if err := scoped.Set(clean, pw); err != nil {
			return copied, err
		}
	}
	return copied, nil
}

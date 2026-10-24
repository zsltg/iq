package cmd

import (
	"errors"
	"fmt"
	"net/url"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/secret"
)

// keyringStore is the credential store behind every keyring call. It is a
// package variable so that a test can swap in an in-memory fake and never touch
// the real OS keyring. Production wiring is the OS-backed store. A command does
// not call it directly. It calls keyringFor, which scopes the accounts to one
// config file.
var keyringStore secret.Keyring = secret.OSKeyring{}

// fileKeyring is the keyring of one config file. It maps the handle of a source
// to a keyring account, so that two config files that share a handle do not
// share an entry. When the file has a keyring ID, the account is
// "<keyring_id>/<handle>". When it has none, the account is the handle alone.
// That is the account of an iq version that had no ID, the legacy account.
// It reads cf.KeyringID at each call, so a new ID takes effect at once. It
// implements secret.Keyring.
type fileKeyring struct {
	cf    *iqconfig.Config
	store secret.Keyring
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

// Get reads the account of this file. When the file has an ID and that account
// has no entry, it reads the legacy account. That covers a copy that stopped
// part way. Only a missing entry falls back. Any other error is returned.
func (k *fileKeyring) Get(handle string) (string, error) {
	pw, err := k.store.Get(k.account(handle))
	if k.cf.KeyringID == "" || !errors.Is(err, secret.ErrNotFound) {
		return pw, err
	}
	return k.store.Get(handle)
}

// GetExact reads the account of this file and never falls back to the legacy
// account.
func (k *fileKeyring) GetExact(handle string) (string, error) {
	return k.store.Get(k.account(handle))
}

// Delete removes the account of this file. It never removes the legacy account.
func (k *fileKeyring) Delete(handle string) error {
	return k.store.Delete(k.account(handle))
}

// GetLegacy reads the legacy account, the handle alone.
func (k *fileKeyring) GetLegacy(handle string) (string, error) {
	return k.store.Get(handle)
}

// DeleteLegacy removes the legacy account, the handle alone.
func (k *fileKeyring) DeleteLegacy(handle string) error {
	return k.store.Delete(handle)
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
	id, err := iqconfig.NewKeyringID()
	if err != nil {
		return noop, err
	}
	scoped := &fileKeyring{cf: &iqconfig.Config{KeyringID: id}, store: keyringStore}
	var copied []string
	undo := func() {
		for _, h := range copied {
			_ = scoped.Delete(h)
		}
		cf.KeyringID = ""
	}
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
			undo()
			return noop, err
		}
		if err := scoped.Set(clean, pw); err != nil {
			copied = append(copied, clean) // a failed Set can leave an entry
			undo()
			return noop, err
		}
		copied = append(copied, clean)
	}
	cf.KeyringID = id
	return undo, nil
}

// effectiveURL returns the connection URI to open for src: src.URL as stored.
// If the source is keyring-backed, it fetches the password from kr under handle
// and splices it back into the URI. The handle argument is the full resolved
// handle.
func effectiveURL(kr secret.Keyring, src iqconfig.Source, handle string) (string, error) {
	if !src.Keyring {
		return src.URL, nil
	}
	pw, err := kr.Get(handle)
	if err != nil {
		return "", err
	}
	return injectPassword(src.URL, pw)
}

// errInvalidURI reports a connection URI that net/url cannot parse.
var errInvalidURI = errors.New("parse URI: the URI is not valid")

// parseURI parses raw with net/url. The error does not quote raw or a part of
// it. The url.Error carries the whole URI, and an EscapeError carries the bad
// escape. Both of these can hold a part of the password.
func parseURI(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err == nil {
		return u, nil
	}
	if _, ok := errors.AsType[url.EscapeError](err); ok {
		return nil, fmt.Errorf("%w: it has a percent sign that does not start a valid escape (write a literal %% as %%25)", errInvalidURI)
	}
	return nil, errInvalidURI
}

// splitPassword returns raw with its password removed, the removed password, and
// whether one was present. Empty userinfo is dropped so the stored URL stays
// clean. It uses net/url like redactURL, which round-trips a multi-host Mongo
// URI.
func splitPassword(raw string) (stripped, password string, ok bool, err error) {
	u, err := parseURI(raw)
	if err != nil {
		return "", "", false, err
	}
	if u.User == nil {
		return raw, "", false, nil
	}
	pw, has := u.User.Password()
	if !has {
		return raw, "", false, nil
	}
	if name := u.User.Username(); name != "" {
		u.User = url.User(name)
	} else {
		u.User = nil
	}
	return u.String(), pw, true, nil
}

// injectPassword returns raw with password set in its userinfo, preserving any
// username. A URL with no username becomes scheme://:password@host, valid for
// Redis AUTH.
func injectPassword(raw, password string) (string, error) {
	u, err := parseURI(raw)
	if err != nil {
		return "", err
	}
	name := ""
	if u.User != nil {
		name = u.User.Username()
	}
	u.User = url.UserPassword(name, password)
	return u.String(), nil
}

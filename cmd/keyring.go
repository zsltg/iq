package cmd

import (
	"errors"
	"fmt"
	"net/url"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/secret"
)

// keyringStore is the credential store the source commands use. It is a package
// variable so a test can swap in an in-memory fake and never touch the real OS
// keyring; production wiring is the OS-backed store.
var keyringStore secret.Keyring = secret.OSKeyring{}

// errKeyringTaken reports that the OS keyring already holds a password for a
// handle. The keyring account is the handle alone, so the entry can belong to a
// source with the same handle in another config file.
var errKeyringTaken = errors.New("the OS keyring already holds a password for this handle, and a source in another config file can use it")

// keyringFree returns nil when the OS keyring holds no password for handle,
// errKeyringTaken when it holds one, and the read error when it cannot be read.
// A command calls it before it writes a new entry, so that it does not replace
// the password of another source. The read and the write are two calls, so two
// iq processes that add the same handle at the same time can both pass it.
func keyringFree(handle string) error {
	_, err := keyringStore.Get(handle)
	switch {
	case err == nil:
		return fmt.Errorf("%s: %w", handle, errKeyringTaken)
	case errors.Is(err, secret.ErrNotFound):
		return nil
	default:
		return err
	}
}

// effectiveURL returns the connection URL to open for src: src.URL as stored,
// unless the source is keyring-backed, in which case the password is fetched
// from the keyring under handle and spliced back into the URL. handle is the
// full resolved handle, the keyring account name.
func effectiveURL(src iqconfig.Source, handle string) (string, error) {
	if !src.Keyring {
		return src.URL, nil
	}
	pw, err := keyringStore.Get(handle)
	if err != nil {
		return "", err
	}
	return injectPassword(src.URL, pw)
}

// errInvalidURI reports a connection URI that net/url cannot parse.
var errInvalidURI = errors.New("parse URI: the URI is not valid")

// parseURI parses raw with net/url. The error does not quote raw or a part of
// it: the url.Error carries the whole URI, and an EscapeError carries the bad
// escape, both of which can hold a part of the password.
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

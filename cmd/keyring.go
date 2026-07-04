package cmd

import (
	"fmt"
	"net/url"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/secret"
)

// keyringStore is the credential store the source commands use. It is a package
// variable so a test can swap in an in-memory fake and never touch the real OS
// keyring; production wiring is the OS-backed store.
var keyringStore secret.Keyring = secret.OSKeyring{}

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

// splitPassword returns raw with its password removed, the removed password, and
// whether one was present. Empty userinfo is dropped so the stored URL stays
// clean. It uses net/url like redactURL, which round-trips a multi-host Mongo
// URI.
func splitPassword(raw string) (stripped, password string, ok bool, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false, fmt.Errorf("parse url: %w", err)
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
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	name := ""
	if u.User != nil {
		name = u.User.Username()
	}
	u.User = url.UserPassword(name, password)
	return u.String(), nil
}

package cmd

import (
	"errors"
	"fmt"
	"io"
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

// passwordPlan says where `iq add` keeps the password of a URI.
type passwordPlan struct {
	raw      string // the URI as given, with its password
	stored   string // the URI to save in the config file
	password string // the password for the keyring
	keyring  bool   // the password goes to the keyring
	explicit bool   // --store was given
}

// planPassword decides where the password of raw goes. The keyring is the
// default store. An explicit --store keyring requires it: a URI with no
// password is an error. Without --store, a URI with no password stays as it is.
// A URI that does not parse is an error for every store, so no source is saved
// that cannot connect.
func planPassword(raw string, keyring, explicit bool) (passwordPlan, error) {
	stripped, pw, ok, err := splitPassword(raw)
	if err != nil {
		return passwordPlan{}, err
	}
	plan := passwordPlan{raw: raw, stored: raw, explicit: explicit}
	switch {
	case !keyring:
	case ok:
		plan.stored, plan.password, plan.keyring = stripped, pw, true
	case explicit:
		return passwordPlan{}, errors.New("--store keyring: URI has no password to store")
	}
	return plan, nil
}

// saveSource keeps the password of the source name as plan says, then saves
// the config file. A password that it wrote to the keyring is deleted again when
// the save fails. The fallback warning goes to stderr only after the save,
// because it says that the password is in the config file.
func saveSource(stderr io.Writer, cf *iqconfig.Config, name string, plan passwordPlan) error {
	inKeyring := false
	if plan.keyring {
		var err error
		if inKeyring, err = keepPassword(cf, name, plan); err != nil {
			return err
		}
	}
	h := iqconfig.CleanHandle(name)
	if err := cf.Save(); err != nil {
		if inKeyring {
			_ = keyringStore.Delete(h)
		}
		return err
	}
	if plan.keyring && !inKeyring {
		// The keyring error can name library internals (a D-Bus object path),
		// so the warning gives only what iq knows and the remedy.
		_, _ = fmt.Fprintf(stderr, "warning: could not write the password of %s to the OS keyring, so it is in the config file\n"+
			"to move it later, unlock or set up the keyring and run: iq config keyring migrate %s\n"+
			"to keep a password in the config file without this warning, add the source with --store inline\n", h, h)
	}
	return nil
}

// keepPassword writes the password of the source name to the OS keyring and
// marks the source keyring-backed, and reports whether the keyring holds it. It
// does not replace an entry that it finds (see keyringFree). When the keyring
// cannot be read or written and --store was not given, the source keeps the URI
// with its password in the config file.
func keepPassword(cf *iqconfig.Config, name string, plan passwordPlan) (bool, error) {
	h := iqconfig.CleanHandle(name)
	err := keyringFree(h)
	if errors.Is(err, errKeyringTaken) {
		return false, fmt.Errorf("%w; choose another handle with -n", err)
	}
	if err == nil {
		err = keyringStore.Set(h, plan.password)
	}
	if err == nil {
		if err := cf.UseKeyring(name); err != nil {
			_ = keyringStore.Delete(h)
			return false, err
		}
		return true, nil
	}
	if plan.explicit {
		return false, err
	}
	if err := cf.SetSourceURL(name, plan.raw); err != nil {
		return false, err
	}
	return false, nil
}

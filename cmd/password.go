package cmd

import (
	"errors"
	"fmt"
	"io"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/secret"
)

// This file holds where `iq add` and `iq config keyring migrate` keep a source
// password. It has three parts. The first is the plan for one add. The second
// is the keyring write with its fallback. The third is the check that does not
// replace a keyring entry of another source.

// errKeyringTaken reports that the OS keyring already holds a password for a
// handle in this config file.
var errKeyringTaken = errors.New("the OS keyring already holds a password for this handle in this config file")

// keyringFree returns nil when the keyring holds no password for handle in the
// config file of kr. It returns errKeyringTaken when it holds one, and the read
// error when the keyring cannot be read. A command calls it before it writes a
// new entry, so that it does not replace the password of another source. A file
// that has no keyring ID has no entry yet, because the first write gives it a
// new ID. The read and the write are two calls. Two iq processes that edit the
// same config file at the same time can both pass it, like any two edits of one
// config file.
func keyringFree(kr *fileKeyring, handle string) error {
	if kr.cf.KeyringID == "" {
		return nil
	}
	_, err := kr.GetExact(handle)
	switch {
	case err == nil:
		return fmt.Errorf("%s: %w", handle, errKeyringTaken)
	case errors.Is(err, secret.ErrNotFound):
		return nil
	default:
		return err
	}
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
// the save fails, and so is a keyring ID that it assigned. The fallback warning
// goes to stderr only after the save, because it says that the password is in
// the config file.
func saveSource(stderr io.Writer, cf *iqconfig.Config, name string, plan passwordPlan) error {
	inKeyring, rollback, err := keepPassword(cf, name, plan)
	if err != nil {
		return err
	}
	h := iqconfig.CleanHandle(name)
	if err := cf.Save(); err != nil {
		if inKeyring {
			_ = keyringFor(cf).Delete(h)
		}
		rollback()
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
// marks the source keyring-backed, and reports whether the keyring holds it.
// The returned function undoes a keyring ID that the call assigned, and does
// nothing otherwise. When the plan does not use the keyring, it does nothing.
// It does not replace an entry that it finds (see keyringFree). When the
// keyring cannot be read or written and --store was not given, the source keeps
// the URI with its password in the config file, and the ID assignment is undone.
func keepPassword(cf *iqconfig.Config, name string, plan passwordPlan) (bool, func(), error) {
	noop := func() {}
	if !plan.keyring {
		return false, noop, nil
	}
	h := iqconfig.CleanHandle(name)
	rollback, err := ensureKeyringID(cf)
	if err == nil {
		kr := keyringFor(cf)
		err = keyringFree(kr, h)
		if errors.Is(err, errKeyringTaken) {
			rollback()
			return false, noop, fmt.Errorf("%w; choose another handle with -n", err)
		}
		if err == nil {
			err = kr.Set(h, plan.password)
		}
		if err == nil {
			if err := cf.UseKeyring(name); err != nil {
				_ = kr.Delete(h)
				rollback()
				return false, noop, err
			}
			return true, rollback, nil
		}
		rollback()
	}
	if plan.explicit {
		return false, noop, err
	}
	if err := cf.SetSourceURL(name, plan.raw); err != nil {
		return false, noop, err
	}
	return false, noop, nil
}

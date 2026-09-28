//go:build !windows

package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ModeWarning returns a warning when the config file holds an inline password
// and other users can access it (a mode wider than 0600), or "" when there is
// nothing to report. Save writes the file with mode 0600, but an editor or a copy
// can widen it. A file that cannot be read or parsed gives no warning: Load
// reports that error. Windows has no such mode bits (modewarning_windows.go).
func ModeWarning() string {
	p, err := Path()
	if err != nil {
		return ""
	}
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm()&0o077 == 0 {
		return ""
	}
	c, err := Load()
	if err != nil || !c.hasInlinePassword() {
		return ""
	}
	return fmt.Sprintf("warning: the config file %s holds an inline password and other users can access it (mode %04o); run: chmod 600 %s",
		p, info.Mode().Perm(), shellQuote(p))
}

// shellQuote returns s as one POSIX shell word, so a suggested command works for
// a path with spaces (the default macOS config path has one) or quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hasInlinePassword reports whether a source URL in the config file carries a
// password. It walks the sources in handle order, so the result never depends on
// map order.
func (c *Config) hasInlinePassword() bool {
	for _, h := range c.List() {
		if hasPassword(h.Source.URL) {
			return true
		}
	}
	return false
}

// hasPassword reports whether raw has a password in its userinfo. A URL that
// net/url rejects (a bad escape, for example) can still hold a password, so it
// falls back to a plain scan: text before the last "@" of the authority part
// that contains a ":". The fallback can report a password that is not there,
// which only adds a warning, and it never misses one.
func hasPassword(raw string) bool {
	if u, err := url.Parse(raw); err == nil {
		_, ok := u.User.Password() // nil-safe: no user info has no password
		return ok
	}
	_, rest, found := strings.Cut(raw, "://")
	if !found {
		return false
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	userinfo := rest[:max(strings.LastIndex(rest, "@"), 0)]
	return strings.Contains(userinfo, ":")
}

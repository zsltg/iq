// Package config persists the CLI's named connection sources and which source
// and group are active, so a query need not repeat a connection URL. It stores
// no secrets of its own, but a source URL may carry credentials, so the file is
// written 0600. The backend is inferred from a source's URL scheme, so this
// package stays driver-agnostic: it never imports a driver and never validates
// which schemes are supported (that belongs to the CLI's composition root).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// EnvConfig is the environment variable that overrides the config file path.
const EnvConfig = "IQ_CONFIG"

// Validation errors, exported as sentinels so callers can match with errors.Is.
var (
	// ErrEmptyHandle is returned when a source name is blank.
	ErrEmptyHandle = errors.New("empty source name")
	// ErrBadHandle is returned when a source name uses illegal characters.
	ErrBadHandle = errors.New("invalid source name")
	// ErrDuplicate is returned when adding a source name that already exists.
	ErrDuplicate = errors.New("source already exists")
	// ErrEmptyURL is returned when a source is added with a blank URL.
	ErrEmptyURL = errors.New("empty source url")
	// ErrUnknownSource is returned when a named source does not exist.
	ErrUnknownSource = errors.New("unknown source")
	// ErrUnknownGroup is returned when a group has no sources.
	ErrUnknownGroup = errors.New("unknown group")
)

// Source is a named connection target. The backend is inferred from the URL
// scheme, so no driver field is stored. Collection applies to MongoDB only and
// is empty otherwise. When Keyring is set the source's password lives in the OS
// keyring rather than in URL, and the composition root splices it back in at
// connect time.
type Source struct {
	URL        string `toml:"url"`
	Collection string `toml:"collection,omitempty"`
	Keyring    bool   `toml:"keyring,omitempty"`
}

// Config is the persisted CLI state: the named sources keyed by their full
// handle, the active source, and the active group (each empty when unset).
type Config struct {
	Active  string            `toml:"active,omitempty"`
	Group   string            `toml:"group,omitempty"`
	Sources map[string]Source `toml:"sources,omitempty"`
}

// Handle pairs a source with its full name, for listing.
type Handle struct {
	Name   string
	Source Source
}

// Path returns the config file path: $IQ_CONFIG when set, otherwise
// <os.UserConfigDir>/iq/iq.toml.
func Path() (string, error) {
	if p := os.Getenv(EnvConfig); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(dir, "iq", "iq.toml"), nil
}

// Load reads and parses the config file. A missing file yields an empty Config
// with an initialized Sources map and no error, so first use needs no setup.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{Sources: map[string]Source{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", p, err)
	}
	var c Config
	if err := toml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", p, err)
	}
	if c.Sources == nil {
		c.Sources = map[string]Source{}
	}
	return &c, nil
}

// Save writes c to Path() atomically (a temp file in the same directory then a
// rename) with 0600 permissions, creating the parent directory (0700) if needed.
func (c *Config) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	f, err := os.CreateTemp(dir, "iq-*.toml")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmp := f.Name()
	// Remove the temp file on any error path; a no-op once the rename succeeds.
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("set config permissions: %w", err)
	}
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		_ = f.Close()
		return fmt.Errorf("encode config: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// Add registers a new source. It errors on a blank or malformed name, a name
// that already exists, or a blank URL. Scheme support is validated by the
// caller, which owns backend dispatch.
func (c *Config) Add(handle, url, collection string) error {
	h, err := validateHandle(handle)
	if err != nil {
		return err
	}
	if strings.TrimSpace(url) == "" {
		return ErrEmptyURL
	}
	if _, ok := c.Sources[h]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicate, h)
	}
	if c.Sources == nil {
		c.Sources = map[string]Source{}
	}
	c.Sources[h] = Source{URL: url, Collection: collection}
	return nil
}

// Rename records a source key that Move relocated, so a caller can migrate any
// external per-handle state such as a keyring credential.
type Rename struct {
	Old string
	New string
}

// Move renames a source or a whole group. When old is a source, it is renamed to
// the full handle new. When old is a group (a handle prefix with members), every
// member is re-prefixed from old to new. A source with the same name as a group
// is treated as the source. Move keeps the active source and group pointing at
// their new handles, and clears the active group if it ends up empty. It errors
// if old is unknown, new is malformed, or new collides with an existing source.
// It returns the old→new handle pairs it moved.
func (c *Config) Move(old, new string) ([]Rename, error) {
	oldC := cleanHandle(old)
	if oldC == "" {
		return nil, ErrEmptyHandle
	}
	newC, err := validateHandle(new)
	if err != nil {
		return nil, err
	}
	if oldC == newC {
		return nil, nil
	}
	if _, ok := c.Sources[oldC]; ok {
		return c.moveSource(oldC, newC)
	}
	if c.hasGroup(oldC) {
		return c.moveGroup(oldC, newC)
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownSource, oldC)
}

// moveSource renames a single source from oldC to newC, both already cleaned.
func (c *Config) moveSource(oldC, newC string) ([]Rename, error) {
	if _, exists := c.Sources[newC]; exists {
		return nil, fmt.Errorf("%w: %q", ErrDuplicate, newC)
	}
	c.Sources[newC] = c.Sources[oldC]
	delete(c.Sources, oldC)
	if c.Active == oldC {
		c.Active = newC
	}
	if c.Group != "" && !c.hasGroup(c.Group) {
		c.Group = ""
	}
	return []Rename{{Old: oldC, New: newC}}, nil
}

// moveGroup re-prefixes every member of group oldC to newC, both already cleaned.
func (c *Config) moveGroup(oldC, newC string) ([]Rename, error) {
	oldPrefix := oldC + "/"
	members := make([]string, 0)
	member := make(map[string]bool)
	for k := range c.Sources {
		if strings.HasPrefix(k, oldPrefix) {
			members = append(members, k)
			member[k] = true
		}
	}
	// Reject a target that already names a source outside the moved set.
	for _, k := range members {
		nk := newC + "/" + strings.TrimPrefix(k, oldPrefix)
		if _, exists := c.Sources[nk]; exists && !member[nk] {
			return nil, fmt.Errorf("%w: %q", ErrDuplicate, nk)
		}
	}
	moved := make([]Rename, 0, len(members))
	for _, k := range members {
		nk := newC + "/" + strings.TrimPrefix(k, oldPrefix)
		c.Sources[nk] = c.Sources[k]
		delete(c.Sources, k)
		if c.Active == k {
			c.Active = nk
		}
		moved = append(moved, Rename{Old: k, New: nk})
	}
	// Re-point the active group at its new prefix. It cannot become empty here:
	// every source in it was just re-prefixed, so the remapped group still has
	// members (moveSource handles the emptying case).
	switch {
	case c.Group == oldC:
		c.Group = newC
	case strings.HasPrefix(c.Group, oldPrefix):
		c.Group = newC + "/" + strings.TrimPrefix(c.Group, oldPrefix)
	}
	return moved, nil
}

// UseKeyring marks the named source as keyring-backed: its password lives in the
// OS keyring, not the stored URL. It errors if the source is unknown. The caller
// owns storing the password in the keyring; this only records the flag.
func (c *Config) UseKeyring(handle string) error {
	h := cleanHandle(handle)
	s, ok := c.Sources[h]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSource, h)
	}
	s.Keyring = true
	c.Sources[h] = s
	return nil
}

// Remove deletes the named source, clearing the active source if it pointed
// there and the active group if that group no longer has any source. It errors
// if the name is unknown.
func (c *Config) Remove(handle string) error {
	h := cleanHandle(handle)
	if _, ok := c.Sources[h]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSource, h)
	}
	delete(c.Sources, h)
	if c.Active == h {
		c.Active = ""
	}
	if c.Group != "" && !c.hasGroup(c.Group) {
		c.Group = ""
	}
	return nil
}

// Removed records a source that RemoveAll deleted, so a caller can clean up
// external per-handle state such as a keyring credential.
type Removed struct {
	Handle string
	Source Source
}

// RemoveAll deletes every named source and group atomically: if any name is
// neither a known source nor a non-empty group, nothing is removed and the error
// names each unknown one. A group name removes all of its members. It returns
// what it removed (sorted by handle, de-duplicated across overlapping names),
// clearing the active source if removed and the active group if it ends up empty.
func (c *Config) RemoveAll(names []string) ([]Removed, error) {
	set := make(map[string]Source)
	var unknown []string
	for _, name := range names {
		h := cleanHandle(name)
		if s, ok := c.Sources[h]; ok {
			set[h] = s
			continue
		}
		prefix := h + "/"
		found := false
		for k, s := range c.Sources {
			if strings.HasPrefix(k, prefix) {
				set[k] = s
				found = true
			}
		}
		if !found {
			unknown = append(unknown, h)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnknownSource, strings.Join(unknown, ", "))
	}
	handles := make([]string, 0, len(set))
	for h := range set {
		handles = append(handles, h)
	}
	sort.Strings(handles)
	removed := make([]Removed, 0, len(handles))
	for _, h := range handles {
		removed = append(removed, Removed{Handle: h, Source: set[h]})
	}
	for _, r := range removed {
		delete(c.Sources, r.Handle)
		if c.Active == r.Handle {
			c.Active = ""
		}
	}
	if c.Group != "" && !c.hasGroup(c.Group) {
		c.Group = ""
	}
	return removed, nil
}

// SetActive sets the active source, storing its resolved full handle. It errors
// if the name resolves to no source.
func (c *Config) SetActive(handle string) error {
	_, full, ok := c.Resolve(handle)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSource, cleanHandle(handle))
	}
	c.Active = full
	return nil
}

// SetGroup sets the active group. An empty group clears it. It errors if the
// group has no sources.
func (c *Config) SetGroup(group string) error {
	g, err := validateHandle(group)
	if err != nil {
		if errors.Is(err, ErrEmptyHandle) {
			c.Group = ""
			return nil
		}
		return err
	}
	if !c.hasGroup(g) {
		return fmt.Errorf("%w: %q", ErrUnknownGroup, g)
	}
	c.Group = g
	return nil
}

// Resolve looks up a source by name, applying active-group namespacing: a name
// containing "/" is absolute; otherwise, when a group is active, "<group>/<name>"
// is tried first and the bare name second. It returns the source, the full
// handle it matched, and whether it was found.
func (c *Config) Resolve(name string) (Source, string, bool) {
	name = cleanHandle(name)
	if name == "" {
		return Source{}, "", false
	}
	if !strings.Contains(name, "/") && c.Group != "" {
		full := c.Group + "/" + name
		if s, ok := c.Sources[full]; ok {
			return s, full, true
		}
	}
	if s, ok := c.Sources[name]; ok {
		return s, name, true
	}
	return Source{}, "", false
}

// List returns the sources sorted by full handle, for `iq ls`.
func (c *Config) List() []Handle {
	hs := make([]Handle, 0, len(c.Sources))
	for name, s := range c.Sources {
		hs = append(hs, Handle{Name: name, Source: s})
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].Name < hs[j].Name })
	return hs
}

// Groups returns every distinct group, sorted: each ancestor path prefix of a
// grouped source handle (so "a/b/c" contributes "a" and "a/b"). These are exactly
// the names SetGroup accepts. A top-level source contributes no group.
func (c *Config) Groups() []string {
	seen := make(map[string]bool)
	for h := range c.Sources {
		parts := strings.Split(h, "/")
		for i := 1; i < len(parts); i++ {
			seen[strings.Join(parts[:i], "/")] = true
		}
	}
	groups := make([]string, 0, len(seen))
	for g := range seen {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	return groups
}

// CountGroup returns how many sources belong to the given group (any source
// whose handle is prefixed by "<group>/").
func (c *Config) CountGroup(group string) int {
	prefix := cleanHandle(group) + "/"
	n := 0
	for h := range c.Sources {
		if strings.HasPrefix(h, prefix) {
			n++
		}
	}
	return n
}

// hasGroup reports whether any source belongs to the given group.
func (c *Config) hasGroup(group string) bool {
	prefix := group + "/"
	for h := range c.Sources {
		if strings.HasPrefix(h, prefix) {
			return true
		}
	}
	return false
}

// CleanHandle canonicalizes a handle the way stored keys are: trimming space and
// a single leading "@". The composition root uses it to derive the keyring
// account name that matches a source's stored key.
func CleanHandle(h string) string {
	return cleanHandle(h)
}

// cleanHandle trims surrounding space and a single leading "@" (accepted for
// sq muscle memory; handles are stored without it).
func cleanHandle(h string) string {
	return strings.TrimPrefix(strings.TrimSpace(h), "@")
}

// validateHandle cleans and checks a handle: non-empty, no leading, trailing, or
// doubled "/", and only letters, digits, ".", "_", "-", "/". The "/" separates
// group from name.
func validateHandle(h string) (string, error) {
	h = cleanHandle(h)
	if h == "" {
		return "", ErrEmptyHandle
	}
	if strings.HasPrefix(h, "/") || strings.HasSuffix(h, "/") || strings.Contains(h, "//") {
		return "", fmt.Errorf("%w %q: misplaced '/'", ErrBadHandle, h)
	}
	for _, r := range h {
		if !isHandleRune(r) {
			return "", fmt.Errorf("%w %q: only letters, digits, '.', '_', '-', '/' allowed", ErrBadHandle, h)
		}
	}
	return h, nil
}

// isHandleRune reports whether r is allowed in a handle.
func isHandleRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '.', r == '_', r == '-', r == '/':
		return true
	default:
		return false
	}
}

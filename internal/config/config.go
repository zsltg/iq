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
	"net/url"
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
	// ErrEmptyOption is returned when an option key is blank.
	ErrEmptyOption = errors.New("empty option name")
)

// Source is a named connection target. The backend is inferred from the URL
// scheme, so no driver field is stored. A driver-specific default (e.g. a
// MongoDB collection) rides in the URL as a query param the driver owns. When
// Keyring is set the source's password lives in the OS keyring rather than in
// URL, and the composition root splices it back in at connect time.
type Source struct {
	URL string `toml:"url"`
	// Collection is deprecated: it is folded into the URL as ?collection= on load
	// and never written back. It remains only so a config file written before
	// drivers owned their URL params still parses and migrates cleanly.
	Collection string `toml:"collection,omitempty"`
	Keyring    bool   `toml:"keyring,omitempty"`
	// Options are this source's stored flag defaults, keyed by flag name and held
	// as the flag's canonical string form. They override the base Options for a
	// query that targets this source. The config package never validates a key or
	// value: the CLI owns the persistable-option allowlist and parsing.
	Options map[string]string `toml:"options,omitempty"`
}

// Config is the persisted CLI state: the named sources keyed by their full
// handle, the active source, the active group (each empty when unset), and the
// base options — stored flag defaults that apply when no source overrides them.
type Config struct {
	Active  string            `toml:"active,omitempty"`
	Group   string            `toml:"group,omitempty"`
	Options map[string]string `toml:"options,omitempty"`
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
	data, err := os.ReadFile(p) //nolint:gosec // G304: reads the user's config file at a resolved path, by design.
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
	c.migrateCollections()
	return &c, nil
}

// migrateCollections folds a legacy per-source `collection` field into the URL as
// ?collection=, the driver-owned form, then clears the field so nothing else
// reads it and the next Save drops it (omitempty). It is a one-time compat shim
// for config files written before drivers owned their URL params.
func (c *Config) migrateCollections() {
	for h, s := range c.Sources {
		if s.Collection == "" {
			continue
		}
		s.URL = appendCollection(s.URL, s.Collection)
		s.Collection = ""
		c.Sources[h] = s
	}
}

// appendCollection returns rawURL with ?collection=collection added, unless it
// already carries a collection param or does not parse (left as-is; a connect
// later surfaces any malformed URL).
func appendCollection(rawURL, collection string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	if q.Get("collection") != "" {
		return rawURL
	}
	q.Set("collection", collection)
	u.RawQuery = q.Encode()
	return u.String()
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
// caller, which owns backend dispatch. A driver-specific default (a MongoDB
// collection) rides in the URL, so it is not a separate argument.
func (c *Config) Add(handle, rawURL string) error {
	h, err := validateHandle(handle)
	if err != nil {
		return err
	}
	if strings.TrimSpace(rawURL) == "" {
		return ErrEmptyURL
	}
	if _, ok := c.Sources[h]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicate, h)
	}
	if c.Sources == nil {
		c.Sources = map[string]Source{}
	}
	c.Sources[h] = Source{URL: rawURL}
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
	c.rekey(oldC, newC)
	c.dropEmptyGroup()
	return []Rename{{Old: oldC, New: newC}}, nil
}

// rekey stores the source under newKey and removes it from oldKey. The active
// source follows when it pointed at oldKey.
func (c *Config) rekey(oldKey, newKey string) {
	c.Sources[newKey] = c.Sources[oldKey]
	delete(c.Sources, oldKey)
	if c.Active == oldKey {
		c.Active = newKey
	}
}

// dropEmptyGroup clears the active group when no source belongs to it any more.
func (c *Config) dropEmptyGroup() {
	if c.Group != "" && !c.hasGroup(c.Group) {
		c.Group = ""
	}
}

// moveGroup re-prefixes every member of group oldC to newC, both already cleaned.
func (c *Config) moveGroup(oldC, newC string) ([]Rename, error) {
	members := c.membersOf(oldC)
	if err := c.moveCollision(members, oldC, newC); err != nil {
		return nil, err
	}
	moved := make([]Rename, 0, len(members))
	for _, k := range members {
		nk := reprefix(k, oldC, newC)
		c.rekey(k, nk)
		moved = append(moved, Rename{Old: k, New: nk})
	}
	c.repointGroup(oldC, newC)
	return moved, nil
}

// moveCollision returns ErrDuplicate for the first member whose target name
// already names a source outside the moved set.
func (c *Config) moveCollision(members []string, oldGroup, newGroup string) error {
	member := make(map[string]bool, len(members))
	for _, k := range members {
		member[k] = true
	}
	for _, k := range members {
		nk := reprefix(k, oldGroup, newGroup)
		if _, exists := c.Sources[nk]; exists && !member[nk] {
			return fmt.Errorf("%w: %q", ErrDuplicate, nk)
		}
	}
	return nil
}

// repointGroup moves the active group to its new prefix when it is oldGroup or
// lies below it. It cannot become empty here: every source in it was just
// re-prefixed, so the remapped group still has members (moveSource handles the
// emptying case).
func (c *Config) repointGroup(oldGroup, newGroup string) {
	switch {
	case c.Group == oldGroup:
		c.Group = newGroup
	case strings.HasPrefix(c.Group, oldGroup+"/"):
		c.Group = reprefix(c.Group, oldGroup, newGroup)
	}
}

// reprefix returns key with the group prefix oldGroup replaced by newGroup.
func reprefix(key, oldGroup, newGroup string) string {
	return newGroup + "/" + strings.TrimPrefix(key, oldGroup+"/")
}

// UseKeyring marks the named source as keyring-backed: its password lives in the
// OS keyring, not the stored URL. It errors if the source is unknown. The caller
// owns storing the password in the keyring; this only records the flag.
func (c *Config) UseKeyring(handle string) error {
	h, s, err := c.lookup(handle)
	if err != nil {
		return err
	}
	s.Keyring = true
	c.Sources[h] = s
	return nil
}

// ClearKeyring marks the named source as no longer keyring-backed, so a
// subsequent connect reads the stored URL as-is. It errors if the source is
// unknown. The caller owns deleting the password from the keyring; this only
// clears the flag.
func (c *Config) ClearKeyring(handle string) error {
	h, s, err := c.lookup(handle)
	if err != nil {
		return err
	}
	s.Keyring = false
	c.Sources[h] = s
	return nil
}

// SetSourceURL replaces the stored connection URL of the named source, keeping
// its keyring flag and options. It errors if the source is unknown or url is
// blank. Callers use it when migrating an inline password into the keyring,
// rewriting the source to its password-less form.
func (c *Config) SetSourceURL(handle, url string) error {
	h, s, err := c.lookup(handle)
	if err != nil {
		return err
	}
	if strings.TrimSpace(url) == "" {
		return ErrEmptyURL
	}
	s.URL = url
	c.Sources[h] = s
	return nil
}

// Remove deletes the named source, clearing the active source if it pointed
// there and the active group if that group no longer has any source. It errors
// if the name is unknown.
func (c *Config) Remove(handle string) error {
	h, _, err := c.lookup(handle)
	if err != nil {
		return err
	}
	delete(c.Sources, h)
	if c.Active == h {
		c.Active = ""
	}
	c.dropEmptyGroup()
	return nil
}

// lookup returns the cleaned handle and the stored source for handle. An unknown
// handle gives ErrUnknownSource, wrapped with the cleaned handle.
func (c *Config) lookup(handle string) (string, Source, error) {
	h := cleanHandle(handle)
	s, ok := c.Sources[h]
	if !ok {
		return "", Source{}, fmt.Errorf("%w: %q", ErrUnknownSource, h)
	}
	return h, s, nil
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
		members := c.membersOf(h)
		for _, k := range members {
			set[k] = c.Sources[k]
		}
		if len(members) == 0 {
			unknown = append(unknown, h)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnknownSource, strings.Join(unknown, ", "))
	}
	removed := sortedRemoved(set)
	c.forget(removed)
	c.dropEmptyGroup()
	return removed, nil
}

// sortedRemoved returns the entries of set as Removed values, sorted by handle.
func sortedRemoved(set map[string]Source) []Removed {
	handles := make([]string, 0, len(set))
	for h := range set {
		handles = append(handles, h)
	}
	sort.Strings(handles)
	removed := make([]Removed, 0, len(handles))
	for _, h := range handles {
		removed = append(removed, Removed{Handle: h, Source: set[h]})
	}
	return removed
}

// forget deletes the removed sources. The active source is cleared when it was
// one of them.
func (c *Config) forget(removed []Removed) {
	for _, r := range removed {
		delete(c.Sources, r.Handle)
		if c.Active == r.Handle {
			c.Active = ""
		}
	}
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
	seen := make(map[string]struct{})
	for h := range c.Sources {
		parts := strings.Split(h, "/")
		for i := 1; i < len(parts); i++ {
			seen[strings.Join(parts[:i], "/")] = struct{}{}
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
	return len(c.membersOf(cleanHandle(group)))
}

// hasGroup reports whether any source belongs to the given group.
func (c *Config) hasGroup(group string) bool {
	return len(c.membersOf(group)) > 0
}

// membersOf returns the handles of the sources whose name starts with
// "<group>/", in map order.
func (c *Config) membersOf(group string) []string {
	prefix := group + "/"
	var members []string
	for h := range c.Sources {
		if strings.HasPrefix(h, prefix) {
			members = append(members, h)
		}
	}
	return members
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
	// Padding with "/" makes a leading, a trailing and a doubled slash all show as "//".
	if strings.Contains("/"+h+"/", "//") {
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

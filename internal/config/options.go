package config

import (
	"fmt"
	"sort"
)

// Option pairs a stored option key with its value, for listing.
type Option struct {
	Key   string
	Value string
}

// SetOption stores an option value. An empty handle targets the base config; a
// non-empty one targets that source (resolved like a query would, so an "@"
// prefix or a group-relative name works), and errors if it names no source. The
// key must be non-empty; the value is stored verbatim. The config package does
// not know which keys or values are valid — the CLI validates both before it
// calls here, mirroring how URL-scheme support lives in the CLI, not here.
func (c *Config) SetOption(handle, key, value string) error {
	if key == "" {
		return ErrEmptyOption
	}
	if handle == "" {
		if c.Options == nil {
			c.Options = map[string]string{}
		}
		c.Options[key] = value
		return nil
	}
	src, full, ok := c.Resolve(handle)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSource, cleanHandle(handle))
	}
	if src.Options == nil {
		src.Options = map[string]string{}
	}
	src.Options[key] = value
	c.Sources[full] = src
	return nil
}

// GetOption returns a stored option value and whether it was set. An empty
// handle reads the base config; a non-empty one reads that source (returning
// false, with no error, when the source or the key is absent).
func (c *Config) GetOption(handle, key string) (string, bool) {
	m, ok := c.optionsFor(handle)
	if !ok {
		return "", false
	}
	v, ok := m[key]
	return v, ok
}

// UnsetOption removes a stored option, reporting whether it was present. An
// empty handle targets the base config; a non-empty one targets that source and
// errors if it names no source.
func (c *Config) UnsetOption(handle, key string) (bool, error) {
	if handle == "" {
		if _, ok := c.Options[key]; !ok {
			return false, nil
		}
		delete(c.Options, key)
		return true, nil
	}
	src, full, ok := c.Resolve(handle)
	if !ok {
		return false, fmt.Errorf("%w: %q", ErrUnknownSource, cleanHandle(handle))
	}
	if _, ok := src.Options[key]; !ok {
		return false, nil
	}
	delete(src.Options, key)
	c.Sources[full] = src
	return true, nil
}

// OptionList returns the stored options at the given scope, sorted by key. An
// empty handle lists the base config; a non-empty one lists that source and
// errors if it names no source.
func (c *Config) OptionList(handle string) ([]Option, error) {
	m, ok := c.optionsFor(handle)
	if handle != "" && !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSource, cleanHandle(handle))
	}
	opts := make([]Option, 0, len(m))
	for k, v := range m {
		opts = append(opts, Option{Key: k, Value: v})
	}
	sort.Slice(opts, func(i, j int) bool { return opts[i].Key < opts[j].Key })
	return opts, nil
}

// optionsFor returns the option map for a scope and whether the scope exists. An
// empty handle is the base config (always exists); a non-empty one is a source,
// existing only when resolved.
func (c *Config) optionsFor(handle string) (map[string]string, bool) {
	if handle == "" {
		return c.Options, true
	}
	src, _, ok := c.Resolve(handle)
	if !ok {
		return nil, false
	}
	return src.Options, true
}

package query

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"unicode/utf8"
)

// UTF8Guard makes sure that the text of a write is valid UTF-8. A store that writes
// text as JSON, or as an SDK string, replaces each invalid byte with U+FFFD. So two
// different keys or names would become one. Such a store calls the guard before it
// writes. A store that keeps the bytes exactly does not call it. Driver names the
// backend in each error.
type UTF8Guard struct{ Driver string }

// Key makes sure that a record key is valid UTF-8.
func (g UTF8Guard) Key(key string) error {
	if utf8.ValidString(key) {
		return nil
	}
	return fmt.Errorf("%s: record key %q is not valid UTF-8; the backend would change it to U+FFFD", g.Driver, key)
}

// Keys makes sure that every key in keys is valid UTF-8. A store that deletes by a
// key sent as JSON or as an SDK string calls it before the first delete, so a key
// like "\xff" cannot delete the item stored under "\uFFFD".
func (g UTF8Guard) Keys(keys []string) error {
	for _, k := range keys {
		if err := g.Key(k); err != nil {
			return err
		}
	}
	return nil
}

// KeyValue makes sure that a record key and the value stored under it are valid
// UTF-8. The key is checked first.
func (g UTF8Guard) KeyValue(key string, value any) error {
	if err := g.Key(key); err != nil {
		return err
	}
	return g.Value(key, value)
}

// Value makes sure that every string in a record value is valid UTF-8, both the
// string values and the names of the objects. It looks inside objects and arrays.
// The error names the field path, like "a.b[2]". key is the record key.
func (g UTF8Guard) Value(key string, value any) error {
	if validUTF8Tree(value) {
		return nil
	}
	return utf8Walk{driver: g.Driver, key: key}.value("", value)
}

// validUTF8Tree is the fast yes or no check of Value. It builds no path and sorts
// no names, so the usual valid record costs little.
func validUTF8Tree(value any) bool {
	switch t := value.(type) {
	case string:
		return utf8.ValidString(t)
	case map[string]any:
		for name, v := range t {
			if !utf8.ValidString(name) || !validUTF8Tree(v) {
				return false
			}
		}
	case []any:
		return !slices.ContainsFunc(t, func(e any) bool { return !validUTF8Tree(e) })
	}
	return true
}

// utf8Walk finds the first invalid string in the value of one record. It carries
// the backend and the record key, which every error names.
type utf8Walk struct{ driver, key string }

// value checks the value at path. The top level has an empty path.
func (w utf8Walk) value(path fieldPath, value any) error {
	switch t := value.(type) {
	case string:
		return w.text(path, t)
	case map[string]any:
		return w.object(path, t)
	case []any:
		return w.array(path, t)
	}
	return nil
}

// text checks the string s at path.
func (w utf8Walk) text(path fieldPath, s string) error {
	if utf8.ValidString(s) {
		return nil
	}
	return fmt.Errorf("%s: key %q: %s holds a string that is not valid UTF-8; the backend would change it to U+FFFD",
		w.driver, w.key, path.label())
}

// array checks the elements of the array at path in order.
func (w utf8Walk) array(path fieldPath, list []any) error {
	for i, e := range list {
		if err := w.value(path.element(i), e); err != nil {
			return err
		}
	}
	return nil
}

// object checks the names and the values of the object at path. It goes through
// the names in sorted order, so the same input always gives the same error.
func (w utf8Walk) object(path fieldPath, obj map[string]any) error {
	for _, name := range slices.Sorted(maps.Keys(obj)) {
		at := path.field(name)
		if !utf8.ValidString(name) {
			return fmt.Errorf("%s: key %q: the field name %q is not valid UTF-8; the backend would change it to U+FFFD",
				w.driver, w.key, string(at))
		}
		if err := w.value(at, obj[name]); err != nil {
			return err
		}
	}
	return nil
}

// fieldPath names a value inside a record value, like "a.b[2]". The top level has
// an empty path.
type fieldPath string

// field is the path of the entry name in the object at p.
func (p fieldPath) field(name string) fieldPath {
	if p == "" {
		return fieldPath(name)
	}
	return p + "." + fieldPath(name)
}

// element is the path of the entry i in the array at p.
func (p fieldPath) element(i int) fieldPath {
	return p + "[" + fieldPath(strconv.Itoa(i)) + "]"
}

// label names the value at p in an error.
func (p fieldPath) label() string {
	if p == "" {
		return "the value"
	}
	return "field " + strconv.Quote(string(p))
}

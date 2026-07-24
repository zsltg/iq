package cmd

import (
	"errors"
	"fmt"
	"strings"

	iqcassandra "github.com/zsltg/iq/drivers/cassandra"
	iqmongo "github.com/zsltg/iq/drivers/mongo"
	iqconfig "github.com/zsltg/iq/internal/config"
)

// errNoSource is returned when no source is selected (no --src and no active
// source). The query path checks for it to fall back to piped stdin as a source.
var errNoSource = errors.New("no source selected")

// resolveSource fills cfg.url and cfg.address from the selected source before the
// store opens. Precedence: the --src flag, then the active source; with neither
// it errors — there is no URL or environment fallback. The name may carry a
// dotted address (--src shop.orders), passed opaquely to the driver; the source's
// own default lives in its URL. openStore stays unchanged.
func resolveSource(cfg *config) error {
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	name := cfg.src
	if name == "" {
		name = cf.Active
	}
	if name == "" {
		return fmt.Errorf("%w; add one with `iq add <url>` then select it with `iq src <name>`", errNoSource)
	}
	base, addr, _ := splitSourceArg(cf, name)
	src, full, ok := cf.Resolve(base)
	if !ok {
		return fmt.Errorf("unknown source %q; run `iq ls`", base)
	}
	u, err := effectiveURL(src, full)
	if err != nil {
		return err
	}
	if err := addressUnsupported(u, addr); err != nil {
		return err
	}
	cfg.url = u
	cfg.source = src
	cfg.handle = full
	cfg.address = addr
	// Log the resolved source with its location redacted — never the raw URL, so
	// a stored credential cannot reach a log file.
	cfg.log().Info("source resolved", "handle", cfg.handle, "driver", schemeOf(cfg.url), "location", redactURL(cfg.url))
	return nil
}

// addressUnsupported rejects a dotted address for a source whose driver takes
// none (only MongoDB is addressable). It centralizes the guard every resolve
// site shares. An empty address is always fine.
func addressUnsupported(rawURL, addr string) error {
	if addr == "" {
		return nil
	}
	if d, ok := driverForScheme(schemeOf(rawURL)); ok && d.addressable {
		return nil
	}
	return fmt.Errorf("%s sources have no collections; drop the %q suffix", driverName(rawURL), addr)
}

// urlAddressUnsupported rejects a source URL that carries a driver-owned address
// param (MongoDB's ?collection=) on a backend that takes none (Redis, file). It
// guards `iq add` so a mistaken default fails fast rather than at connect time.
func urlAddressUnsupported(rawURL string) error {
	c := iqmongo.CollectionFromURI(rawURL)
	if c == "" {
		return nil
	}
	if d, ok := driverForScheme(schemeOf(rawURL)); ok && d.addressable {
		return nil
	}
	return fmt.Errorf("%s sources have no collections; drop the ?collection= from the url", driverName(rawURL))
}

// resolveInspectSource fills cfg from the source named by the inspect positional,
// or --src, or the active source (in that precedence). The chosen name accepts
// sq-style `<source>.<collection>` addressing; the address overrides the source's
// URL default for MongoDB, but is rejected for a backend that takes none (Redis).
func resolveInspectSource(cfg *config, arg string) error {
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	name := arg
	if name == "" {
		name = cfg.src
	}
	if name == "" {
		name = cf.Active
	}
	if name == "" {
		return fmt.Errorf("%w; add one with `iq add <url>` then select it with `iq src <name>`", errNoSource)
	}
	base, addr, _ := splitSourceArg(cf, name)
	src, full, ok := cf.Resolve(base)
	if !ok {
		return fmt.Errorf("unknown source %q; run `iq ls`", base)
	}
	u, err := effectiveURL(src, full)
	if err != nil {
		return err
	}
	if err := addressUnsupported(u, addr); err != nil {
		return err
	}
	cfg.url = u
	cfg.source = src
	cfg.handle = full
	cfg.address = addr
	return nil
}

// mongoCollection returns the effective MongoDB collection for a resolved source:
// the dotted address override when set, else the URL's ?collection= default. Only
// the Mongo-aware inspect path calls it.
func mongoCollection(cfg *config) string {
	if cfg.address != "" {
		return cfg.address
	}
	return iqmongo.CollectionFromURI(cfg.url)
}

// cassandraTarget returns the effective Cassandra keyspace and table for a resolved
// source: the keyspace from the URL path and the table from the dotted address
// override or the URL's ?table= default. Only the Cassandra-aware inspect path calls
// it; a parse error yields empty strings, leaving the store open to surface it.
func cassandraTarget(cfg *config) (keyspace, table string) {
	ks, tbl, err := iqcassandra.Target(cfg.url, cfg.address)
	if err != nil {
		return "", ""
	}
	return ks, tbl
}

// splitSourceArg parses a source argument into a source name and an optional
// collection override. It tries the whole arg as a known source first so a handle
// that legitimately contains a dot (or an @-prefixed one) still resolves; failing
// that it walks the dots right to left and takes the longest prefix that names a
// real source, so the most specific handle still wins while the remainder stays
// whole as the address. The remainder may itself be dotted — Couchbase addresses
// a scope.collection, and a MongoDB collection name may contain a dot — which a
// last-dot split could not express. An empty arg yields an empty name, letting
// the caller fall back to --src or the active source.
func splitSourceArg(cf *iqconfig.Config, arg string) (name, collection string, hasCollection bool) {
	if arg == "" {
		return "", "", false
	}
	if _, _, ok := cf.Resolve(arg); ok {
		return arg, "", false
	}
	// A trailing dot names no collection, so there is nothing to split. Checking
	// it once up front keeps it out of the walk below, where it could only ever
	// match the first step anyway.
	if strings.HasSuffix(arg, ".") {
		return arg, "", false
	}
	for i := strings.LastIndex(arg, "."); i > 0; i = strings.LastIndex(arg[:i], ".") {
		if _, _, ok := cf.Resolve(arg[:i]); ok {
			return arg[:i], arg[i+1:], true
		}
	}
	// No prefix names a source. Fall back to the last-dot split so an unknown
	// source still reports the name the user most likely meant.
	if i := strings.LastIndex(arg, "."); i > 0 {
		return arg[:i], arg[i+1:], true
	}
	return arg, "", false
}

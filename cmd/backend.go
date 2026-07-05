package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/zsltg/iq/internal/query"
)

// store is the backend a command talks to. One adapter satisfies both the jq
// port (KVStore) and the raw port (Store), plus FormatRaw for the raw reply, so
// the CLI is written once against this interface and the composition root picks
// the concrete adapter by URL scheme.
type store interface {
	query.KVStore
	query.Store
	FormatRaw(v any, colored bool) string
}

// openStore connects to the backend named by cfg.url, logging the attempt and
// the outcome at the cmd boundary (the driver is safe; the URL is never logged).
func openStore(ctx context.Context, cfg *config) (store, error) {
	cfg.log().Debug("opening store", "driver", schemeOf(cfg.url))
	st, err := dialStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cfg.log().Info("store opened", "driver", schemeOf(cfg.url))
	return st, nil
}

// dialStore connects to the backend named by cfg.url, dispatching on the URL
// scheme through the drivers registry — the single source of truth for which
// concrete adapters exist.
func dialStore(ctx context.Context, cfg *config) (store, error) {
	scheme := schemeOf(cfg.url)
	if scheme == "" {
		return nil, fmt.Errorf("missing url scheme in %q; %s", redactURL(cfg.url), expectedSchemes())
	}
	d, ok := driverForScheme(scheme)
	if !ok {
		return nil, fmt.Errorf("unsupported url scheme %q; %s", scheme, expectedSchemes())
	}
	return d.open(ctx, cfg)
}

// supportedScheme reports whether url's scheme is one openStore can dispatch. It
// is the single check `iq add` uses to reject a source the CLI cannot open.
func supportedScheme(url string) bool {
	_, ok := driverForScheme(schemeOf(url))
	return ok
}

// schemeOf returns the lowercased scheme of a connection URL — the text before
// "://" — or "" if there is none. It does not use net/url so that a multi-host
// MongoDB URI (mongodb://h1,h2/db), which net/url rejects, still dispatches.
func schemeOf(raw string) string {
	before, _, found := strings.Cut(raw, "://")
	if !found {
		return ""
	}
	return strings.ToLower(before)
}

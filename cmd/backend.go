package cmd

import (
	"context"
	"fmt"
	"strings"

	iqmongo "github.com/zsltg/iq/internal/backend/mongo"
	iqredis "github.com/zsltg/iq/internal/backend/redis"
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

// openStore connects to the backend named by cfg.url, dispatching on the URL
// scheme. It is the composition root: the only place that knows which concrete
// adapters exist.
func openStore(ctx context.Context, cfg *config) (store, error) {
	switch schemeOf(cfg.url) {
	case "redis", "rediss":
		return iqredis.Open(ctx, cfg.url)
	case "mongodb", "mongodb+srv":
		return iqmongo.Open(ctx, cfg.url, cfg.collection)
	case "":
		return nil, fmt.Errorf("missing url scheme in %q; expected redis:// or mongodb://", redactURL(cfg.url))
	default:
		return nil, fmt.Errorf("unsupported url scheme %q; expected redis:// or mongodb://", schemeOf(cfg.url))
	}
}

// supportedScheme reports whether url's scheme is one openStore can dispatch. It
// is the single check `iq add` uses to reject a source the CLI cannot open.
func supportedScheme(url string) bool {
	switch schemeOf(url) {
	case "redis", "rediss", "mongodb", "mongodb+srv":
		return true
	default:
		return false
	}
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

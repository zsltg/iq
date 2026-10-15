package cmd

import (
	"context"
	"fmt"
	"io"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// sourceOpener resolves a source name to a store for the in-filter
// source() function. It caches by resolved handle, so a repeated source()
// reuses one connection. A single goroutine drives the query iterator of
// gojq, so the cache needs no locking. The closeAll method releases every
// opened store.
type sourceOpener struct {
	cf    *iqconfig.Config
	cache map[string]store
	// trace, when non-nil, is threaded into each opened source so a --verbose run
	// traces every cross-source backend command.
	trace io.Writer
	// decimal is threaded into each opened source so --format.decimal applies to
	// every cross-source backend uniformly.
	decimal numfmt.DecimalMode
	// noCache and noCacheIndex are threaded into each opened file:// source so
	// --no-cache and --no-cache-index apply uniformly across a cross-source query.
	noCache      bool
	noCacheIndex bool
}

// newSourceOpener returns an opener backed by the source registry cf. The trace
// argument (can be nil) is passed to each opened source for the --verbose
// command trace. The decimal argument is for --format.decimal. The cache flags
// bypass or unindex the file:// decode cache.
func newSourceOpener(cf *iqconfig.Config, trace io.Writer, decimal numfmt.DecimalMode, noCache, noCacheIndex bool) *sourceOpener {
	return &sourceOpener{cf: cf, cache: map[string]store{}, trace: trace, decimal: decimal, noCache: noCache, noCacheIndex: noCacheIndex}
}

// Open resolves name through the registry (applying active-group namespacing)
// and opens its store. It reuses a store already opened for the same handle
// and address. A dotted name (source("shop.orders")) resolves the base handle
// and passes the address to the driver. The cache key includes the address,
// so source("shop.orders") and source("shop.users") do not alias one
// connection.
func (o *sourceOpener) Open(ctx context.Context, name string) (query.KVStore, error) {
	base, addr, _ := splitSourceArg(o.cf, name)
	src, handle, ok := o.cf.Resolve(base)
	if !ok {
		return nil, fmt.Errorf("unknown source %q; run `iq ls`", base)
	}
	if err := addressUnsupported(src.URL, addr); err != nil {
		return nil, err
	}
	key := handle
	if addr != "" {
		key = handle + "\x00" + addr
	}
	if st, ok := o.cache[key]; ok {
		return st, nil
	}
	st, err := openStore(ctx, &config{url: src.URL, address: addr, trace: o.trace, decimalMode: o.decimal, noCache: o.noCache, noCacheIndex: o.noCacheIndex})
	if err != nil {
		return nil, err
	}
	o.cache[key] = st
	return st, nil
}

// closeAll releases every store opened for the query.
func (o *sourceOpener) closeAll() {
	for _, st := range o.cache {
		_ = st.Close()
	}
}

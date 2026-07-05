package cmd

import (
	"context"
	"fmt"
	"io"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
)

// sourceOpener resolves a source name to a store for the in-filter source()
// function, caching by resolved handle so a repeated source() reuses one
// connection. gojq drives the query iterator on a single goroutine, so the
// cache needs no locking. Every opened store is released by closeAll.
type sourceOpener struct {
	cf    *iqconfig.Config
	cache map[string]store
	// trace, when non-nil, is threaded into each opened source so a --verbose run
	// traces every cross-source backend command.
	trace io.Writer
}

// newSourceOpener returns an opener backed by the source registry cf; trace (may
// be nil) is passed to each opened source for the --verbose command trace.
func newSourceOpener(cf *iqconfig.Config, trace io.Writer) *sourceOpener {
	return &sourceOpener{cf: cf, cache: map[string]store{}, trace: trace}
}

// Open resolves name through the registry (applying active-group namespacing)
// and opens its store, reusing a store already opened for the same handle.
func (o *sourceOpener) Open(ctx context.Context, name string) (query.KVStore, error) {
	src, handle, ok := o.cf.Resolve(name)
	if !ok {
		return nil, fmt.Errorf("unknown source %q; run `iq ls`", name)
	}
	if st, ok := o.cache[handle]; ok {
		return st, nil
	}
	st, err := openStore(ctx, &config{url: src.URL, collection: src.Collection, trace: o.trace})
	if err != nil {
		return nil, err
	}
	o.cache[handle] = st
	return st, nil
}

// closeAll releases every store opened for the query.
func (o *sourceOpener) closeAll() {
	for _, st := range o.cache {
		_ = st.Close()
	}
}

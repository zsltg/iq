package cmd

import (
	"errors"
	"fmt"
	"strings"

	iqmongo "github.com/zsltg/iq/drivers/mongo"
	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
)

// sourceSpec is a source paired with the jq that reduces it. The address half
// names a saved source (with an optional dotted keyspace) and resolves to a
// connectable endpoint. The filter field is the expression run against it,
// where "" means the whole keyspace. Every command that reads several sources
// shares this shape. So `handle`, `handle.keyspace` and `handle=<jq>` mean the
// same wherever they appear.
type sourceSpec struct {
	endpoint
	filter string
}

// collection returns the effective MongoDB collection for this spec: the dotted
// address override when set, else the URL's ?collection= default. Kept here
// because only the Mongo-specific stats layer needs it.
func (s sourceSpec) collection() string {
	if s.address != "" {
		return s.address
	}
	return iqmongo.CollectionFromURI(s.url)
}

// eitherFiltered reports whether either side of a diff carries a jq filter,
// from --filter or from its own spec.
func eitherFiltered(left, right sourceSpec) bool {
	return left.filter != "" || right.filter != ""
}

// runConfig builds the per-source config for a read of this spec. It carries
// the run-wide settings forward, above all decimal mode, which changes what the
// filter computes. So one spec string means the same in `iq` and `iq diff`, and
// a store decorator still logs and traces.
func (s sourceSpec) runConfig(cfg *config) *config {
	return &config{
		url:          s.url,
		address:      s.address,
		trace:        cfg.trace,
		decimalMode:  cfg.decimalMode,
		logger:       cfg.logger,
		noCache:      cfg.noCache,
		noCacheIndex: cfg.noCacheIndex,
	}
}

// runOptions builds the engine options that a spec read runs under. So pushdown
// and the structured logger reach the engine the same way as on a plain query.
func (cfg *config) runOptions() query.RunOptions {
	return query.RunOptions{Compile: !cfg.noCompile, Logger: cfg.logger}
}

// parseSourceSpec resolves one `<source>[=<jq filter>]` spec. The cut is at
// the first "=", never the last. Every realistic filter contains "==", so
// splitting at the right-most "=" cuts into the filter and swallows the
// expression. Everything left of the cut is an address and never reaches gojq.
// Everything right of it is jq and never sees the address. A spec with no "="
// takes defaultFilter, so an unfiltered spelling stays the whole keyspace.
func parseSourceSpec(cf *iqconfig.Config, spec, defaultFilter string) (sourceSpec, error) {
	name, filter, hasFilter := strings.Cut(spec, "=")
	switch {
	case !hasFilter:
		filter = defaultFilter
	case strings.TrimSpace(filter) == "":
		return sourceSpec{}, fmt.Errorf("invalid source %q: empty filter", spec)
	}
	ep, err := resolveSourceSpec(cf, name)
	if err != nil {
		return sourceSpec{}, err
	}
	return sourceSpec{endpoint: ep, filter: filter}, nil
}

// resolveSourceSpec resolves the address half of a spec through the shared
// endpoint resolver, then refuses the file endpoints that resolver also serves.
// A read spec names a registered source: stdin and stdout are destinations for
// a copy, not things a query, diff or schema can address.
func resolveSourceSpec(cf *iqconfig.Config, name string) (endpoint, error) {
	if strings.TrimSpace(name) == "" {
		return endpoint{}, errors.New("invalid source spec: expected <source>[=<jq filter>]")
	}
	// resolveEndpoint serves copy endpoints too, so its miss advises -o and "-".
	// A read spec has no such fallbacks, and says so before delegating.
	base, _, _ := splitSourceArg(cf, name)
	if _, _, ok := cf.Resolve(base); !ok {
		// The example URI is spelled out and not elided. newRedactor trims
		// trailing prose punctuation off a URI match. If the text used
		// "file://..." here, the trim cuts the ellipsis and the text prints as
		// "file:".
		return endpoint{}, fmt.Errorf("unknown source %q; run `iq ls` (register a dump with `iq add file:///path/to/dump.json` to read one)", base)
	}
	ep, err := resolveEndpoint(cf, name, false)
	if err != nil {
		return endpoint{}, err
	}
	if ep.isFile {
		return endpoint{}, fmt.Errorf("%q is not a saved source", name)
	}
	return ep, nil
}

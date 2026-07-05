package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// resolveSource fills cfg.url and cfg.collection from the selected source before
// the store opens. Precedence: the --src flag, then the active source; with
// neither it errors — there is no URL or environment fallback. An explicit
// --collection overrides the source's collection. openStore stays unchanged.
func resolveSource(cmd *cobra.Command, cfg *config) error {
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	name := cfg.src
	if name == "" {
		name = cf.Active
	}
	if name == "" {
		return errors.New("no source selected; add one with `iq add <url>` then select it with `iq src <name>`")
	}
	src, full, ok := cf.Resolve(name)
	if !ok {
		return fmt.Errorf("unknown source %q; run `iq ls`", name)
	}
	u, err := effectiveURL(src, full)
	if err != nil {
		return err
	}
	cfg.url = u
	cfg.source = src
	cfg.handle = full
	if !cmd.Flags().Changed("collection") {
		cfg.collection = src.Collection
	}
	// Log the resolved source with its location redacted — never the raw URL, so
	// a stored credential cannot reach a log file.
	cfg.log().Info("source resolved", "handle", cfg.handle, "driver", schemeOf(cfg.url), "location", redactURL(cfg.url))
	return nil
}

// resolveInspectSource fills cfg from the source named by the inspect positional,
// or --src, or the active source (in that precedence). The positional accepts
// sq-style `<source>.<collection>` addressing; the collection there overrides the
// source's stored one for MongoDB, but is rejected for Redis, which has no
// collections. An explicit --collection flag still wins over both.
func resolveInspectSource(cmd *cobra.Command, cfg *config, arg string) error {
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	name, coll, hasColl := splitSourceArg(cf, arg)
	if name == "" {
		name = cfg.src
	}
	if name == "" {
		name = cf.Active
	}
	if name == "" {
		return errors.New("no source selected; add one with `iq add <url>` then select it with `iq src <name>`")
	}
	src, full, ok := cf.Resolve(name)
	if !ok {
		return fmt.Errorf("unknown source %q; run `iq ls`", name)
	}
	u, err := effectiveURL(src, full)
	if err != nil {
		return err
	}
	if hasColl && strings.HasPrefix(schemeOf(u), "redis") {
		return fmt.Errorf("redis sources have no collections; drop the %q suffix", coll)
	}
	cfg.url = u
	cfg.source = src
	cfg.handle = full
	switch {
	case cmd.Flags().Changed("collection"):
		// An explicit --collection flag wins; leave cfg.collection as it set it.
	case hasColl:
		cfg.collection = coll
	default:
		cfg.collection = src.Collection
	}
	return nil
}

// splitSourceArg parses an inspect positional into a source name and an optional
// collection override. It tries the whole arg as a known source first so a handle
// that legitimately contains a dot (or an @-prefixed one) still resolves; only
// when that fails does it split on the last dot into <source>.<collection>. An
// empty arg yields an empty name, letting the caller fall back to --src or the
// active source.
func splitSourceArg(cf *iqconfig.Config, arg string) (name, collection string, hasCollection bool) {
	if arg == "" {
		return "", "", false
	}
	if _, _, ok := cf.Resolve(arg); ok {
		return arg, "", false
	}
	if i := strings.LastIndex(arg, "."); i > 0 && i < len(arg)-1 {
		return arg[:i], arg[i+1:], true
	}
	return arg, "", false
}

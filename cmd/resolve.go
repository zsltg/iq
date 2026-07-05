package cmd

import (
	"errors"
	"fmt"

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
		return errors.New("no source selected; add one with `iq add <name> <url>` then select it with `iq src <name>`")
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
	return nil
}

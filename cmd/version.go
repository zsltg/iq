package cmd

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Build metadata, overridden at build time via ldflags, for example
// `-X github.com/zsltg/iq/cmd.version=v1.2.3`. Left at these defaults for a
// plain `go build`, buildVersion recovers what it can from the embedded build
// info instead.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// buildVersion returns the binary's version string, preferring the
// ldflags-injected value and falling back to the build info the Go toolchain
// stamps into module-aware builds.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	return resolveVersion(version, info, ok)
}

// resolveVersion picks the most specific version available: the injected value
// when it was set, otherwise the module version, otherwise a dev marker carrying
// the VCS revision. It is pure so every branch is testable without depending on
// how the test binary itself was built.
func resolveVersion(injected string, info *debug.BuildInfo, ok bool) string {
	if injected != "dev" {
		return injected
	}
	if !ok {
		return injected
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	if rev := vcsRevision(info); rev != "" {
		return "dev+" + rev
	}
	return injected
}

// vcsRevision returns the commit hash the toolchain recorded in the build
// settings, or the empty string when it is absent.
func vcsRevision(info *debug.BuildInfo) string {
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}

// newVersionCmd builds the `version` subcommand, which prints the version,
// commit, build date, and Go toolchain version.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Print version, commit, build date, and Go version",
		Example: "  $ iq version",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(),
				"iq %s\n  commit: %s\n  built:  %s\n  go:     %s\n",
				buildVersion(), commit, date, runtime.Version())
			return err
		},
	}
}

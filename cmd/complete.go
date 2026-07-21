package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// Dynamic shell completions. Every function here is offline: it reads only the
// config file (via iqconfig.Load, honoring $IQ_CONFIG) and never opens a driver
// or touches the network, so a <TAB> can never hang or connect. A load error
// yields no candidates rather than a failure — completion is best-effort help,
// not a command that reports errors. Cobra does not prefix-filter a
// ValidArgsFunction's results, so each helper filters by toComplete itself.

// completeSourceHandles completes saved source handles. Used for the positional
// source argument of query-shaped commands (src, ping, inspect, schema, diff)
// and the --src flag.
func completeSourceHandles(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cf, err := iqconfig.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	handles := make([]string, 0, len(cf.Sources))
	for _, h := range cf.List() {
		handles = append(handles, h.Name)
	}
	return withPrefix(handles, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeGroups completes saved source groups (ls, group).
func completeGroups(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cf, err := iqconfig.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return withPrefix(cf.Groups(), toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeHandlesAndGroups completes both source handles and groups, for
// commands that accept either (rm, mv).
func completeHandlesAndGroups(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cf, err := iqconfig.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(cf.Sources))
	for _, h := range cf.List() {
		names = append(names, h.Name)
	}
	names = append(names, cf.Groups()...)
	return withPrefix(names, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeConfigKeys completes the persistable option names (config get/set),
// sharing the one persistableOptions allowlist with validation.
func completeConfigKeys(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return withPrefix(persistableOptions, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeCacheClear completes saved source handles but keeps the shell's file
// completion (ShellCompDirectiveDefault), since `cache clear` accepts a source
// or a dump-file path.
func completeCacheClear(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cf, err := iqconfig.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveDefault
	}
	handles := make([]string, 0, len(cf.Sources))
	for _, h := range cf.List() {
		handles = append(handles, h.Name)
	}
	return withPrefix(handles, toComplete), cobra.ShellCompDirectiveDefault
}

// firstArgOnly restricts a completion function to the first positional argument,
// offering nothing (and no file completion) once one argument is present. It is
// used for commands whose later positionals are free-form values (mv's <new>,
// config set's <value>, keyring set's <value>).
func firstArgOnly(fn cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return fn(cmd, args, toComplete)
	}
}

// withPrefix returns the candidates that have toComplete as a prefix, preserving
// order. An empty toComplete keeps them all.
func withPrefix(candidates []string, toComplete string) []string {
	if toComplete == "" {
		return candidates
	}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if strings.HasPrefix(c, toComplete) {
			out = append(out, c)
		}
	}
	return out
}

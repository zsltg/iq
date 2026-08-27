package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// Dynamic shell completions. Every function here is offline: it reads only the
// config file (via iqconfig.Load, honoring $IQ_CONFIG) and never opens a driver
// or touches the network, so a <TAB> can never hang or connect. It must not read
// the OS keyring either (so never effectiveURL) — a keypress cannot be allowed
// to block on an unlock prompt. A load error yields no candidates rather than a
// failure — completion is best-effort help, not a command that reports errors.
// Cobra does not prefix-filter a ValidArgsFunction's results, so each helper
// filters by toComplete itself.

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

// Static value allowlists for the enum-valued flags. Each mirrors a validator
// that switches over the same set — parseLogLevel, validateErrorFormat,
// numfmt.ParseDecimalMode, iqfile.ParseFormat — which are switches, not slices,
// so the values are restated here and pinned by a round-trip test that feeds
// every candidate back through its validator.
var (
	// decimalModeNames mirrors numfmt.ParseDecimalMode (--format.decimal).
	decimalModeNames = []string{"auto", "number", "string"}
	// logLevelNames mirrors parseLogLevel (--log.level); the WARNING alias is
	// left out, since completion offers the canonical spelling only.
	logLevelNames = []string{"DEBUG", "INFO", "WARN", "ERROR"}
	// textJSONNames serves both --log.format and --error.format, whose
	// validators (newLogHandler, validateErrorFormat) take the same pair.
	textJSONNames = []string{"text", "json"}
	// dumpFormatNames mirrors iqfile.ParseFormat (--from-format), offering the
	// canonical name of each format rather than its aliases.
	dumpFormatNames = []string{"jsonl", "json", "yaml", "mongoexport", "bson", "rdb", "dynamodb-json", "cassandra-csv", "neo4j-json"}
	// passwordStoreNames mirrors the store switch in `iq add` (--store).
	passwordStoreNames = []string{"inline", "keyring"}
	// boolValues completes a boolean option's value for `config set`.
	boolValues = []string{"true", "false"}
)

// enumOptionValues maps a persistable option to its value allowlist, for
// `config set <option> <value>`. An option absent here either takes a free-form
// value (a duration, a path) or is a boolean, handled by optionValues.
var enumOptionValues = map[string][]string{
	"format":         formatNames(),
	"format.decimal": decimalModeNames,
	"log.level":      logLevelNames,
	"log.format":     textJSONNames,
	"error.format":   textJSONNames,
}

// fixedValues completes a static allowlist, the shape every enum-valued flag
// takes. It exists so a registration is one line and the prefix filtering stays
// in withPrefix.
func fixedValues(values ...string) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return withPrefix(values, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// completeCSV adapts a completion function to a comma-separated list flag
// (--only, --section): it completes the segment after the last comma and
// re-attaches the segments already typed, since the shell replaces the whole
// word. A value already in the list is dropped from the candidates, so a long
// selection narrows as it is built. ShellCompDirectiveNoSpace keeps the cursor
// put so another comma and segment can follow.
func completeCSV(fn cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		// LastIndex returns -1 when no comma has been typed yet, which splits into
		// an empty head and the whole word — the same as not splitting at all — so
		// the split needs no guard.
		i := strings.LastIndex(toComplete, ",")
		head, tail := toComplete[:i+1], toComplete[i+1:]
		chosen := make(map[string]bool)
		for s := range strings.SplitSeq(head, ",") {
			if s != "" {
				chosen[s] = true
			}
		}
		values, _ := fn(cmd, args, tail)
		out := make([]string, 0, len(values))
		for _, v := range values {
			if !chosen[v] {
				out = append(out, head+v)
			}
		}
		if len(out) == 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	}
}

// completeInspectOnly completes `inspect --only` / `diff --section` with the
// subcommands of the source's own backend, resolved offline from the stored URL
// scheme. A source that cannot be resolved yields no candidates, per this file's
// best-effort contract.
func completeInspectOnly(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	subs := inspectSubcommands(sourceDriverName(cmd, args))
	return completeCSV(fixedValues(subs...))(cmd, args, toComplete)
}

// sourceDriverName resolves, offline, the driver behind the source a command is
// about to act on: the positional argument when present, else --src, else the
// active source — the precedence resolveInspectSource uses. It reads the stored
// URL directly and must never call effectiveURL, whose keyring read can block or
// prompt for an unlock; a completion runs on a keypress and stays
// non-interactive. An unresolvable source yields an empty name.
func sourceDriverName(cmd *cobra.Command, args []string) string {
	cf, err := iqconfig.Load()
	if err != nil {
		return ""
	}
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	if name == "" && cmd != nil {
		// --src is declared once, on the root's persistent flags; cobra's merge
		// into a subcommand shares the same *Flag, so the root lookup reads the
		// value parsing wrote — and works on an unexecuted command tree too.
		if f := cmd.Root().PersistentFlags().Lookup("src"); f != nil {
			name = f.Value.String()
		}
	}
	if name == "" {
		name = cf.Active
	}
	if name == "" {
		return ""
	}
	base, _, _ := splitSourceArg(cf, name)
	src, _, ok := cf.Resolve(base)
	if !ok {
		return ""
	}
	return driverName(src.URL)
}

// completeConfigSet completes `config set <option> <value>`: the option name for
// the first positional, then that option's own values for the second.
func completeConfigSet(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 1 {
		return withPrefix(optionValues(cmd, args[0]), toComplete), cobra.ShellCompDirectiveNoFileComp
	}
	return firstArgOnly(completeConfigKeys)(cmd, args, toComplete)
}

// optionValues returns the completion candidates for a persistable option's
// value: its enum allowlist when it has one, else true/false when the flag the
// option aliases is a boolean, else nothing (a free-form duration or path). The
// boolean case reads the flag itself, so an option cannot drift from its flag.
func optionValues(cmd *cobra.Command, key string) []string {
	if vals, ok := enumOptionValues[key]; ok {
		return vals
	}
	if cmd == nil {
		return nil
	}
	if f := cmd.Root().Flags().Lookup(key); f != nil && f.Value.Type() == "bool" {
		return boolValues
	}
	return nil
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

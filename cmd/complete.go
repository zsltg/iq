package cmd

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	iqfile "github.com/zsltg/iq/drivers/file"
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

// candidate is one completion proposal: the text the shell inserts and an
// optional description that it shows next to the text. Helpers build candidates
// and candidateStrings turns them into strings, so the output rules live in one
// place.
type candidate struct {
	value string
	desc  string
}

// candidateFunc is a completion function that keeps descriptions apart from
// values. completeCSV needs this form, because it must edit values only.
type candidateFunc func(cmd *cobra.Command, args []string, toComplete string) ([]candidate, cobra.ShellCompDirective)

// asCompletion adapts a candidateFunc to the cobra signature. It is the single
// point where candidates become strings.
func asCompletion(fn candidateFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		cands, dir := fn(cmd, args, toComplete)
		return candidateStrings(cands), dir
	}
}

// candidateStrings turns candidates into the strings that cobra sends to the
// shell. It omits a candidate whose value holds a control character. It never
// changes a value, because a changed value would select a different handle. A
// tab in a value would also split the value from a false description. It
// escapes the control characters in each description, since description text
// can come from a stored URI. Percent-encoded bytes such as %09 are plain text
// and stay as they are.
func candidateStrings(cands []candidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		if strings.IndexFunc(c.value, unicode.IsControl) >= 0 {
			continue
		}
		if c.desc == "" {
			out = append(out, c.value)
			continue
		}
		out = append(out, cobra.CompletionWithDesc(c.value, escapeControl(c.desc)))
	}
	return out
}

// escapeControl replaces each control character with a visible escape: \t, \n,
// and \r by name, and any other one as \xNN. unicode.IsControl matches only
// C0, DEL, and C1, so two hex digits are always enough.
func escapeControl(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// plainCandidates wraps values that carry no description.
func plainCandidates(values []string) []candidate {
	out := make([]candidate, len(values))
	for i, v := range values {
		out[i] = candidate{value: v}
	}
	return out
}

// handleCandidates lists the saved source handles in sorted order. The
// description names the driver, the keyspace, and the active marker. It never
// holds the host, the user, the password, or the URI.
func handleCandidates(cf *iqconfig.Config) []candidate {
	hs := cf.List()
	out := make([]candidate, 0, len(hs))
	for _, h := range hs {
		var parts []string
		if d := driverName(h.Source.URL); d != "" {
			parts = append(parts, d)
		}
		if ks := urlAddressName(h.Source.URL); ks != "" {
			parts = append(parts, ks)
		}
		if h.Name == cf.Active {
			parts = append(parts, "active")
		}
		out = append(out, candidate{value: h.Name, desc: strings.Join(parts, ", ")})
	}
	return out
}

// groupCandidates lists the saved groups, each with its source count.
func groupCandidates(cf *iqconfig.Config) []candidate {
	groups := cf.Groups()
	out := make([]candidate, 0, len(groups))
	for _, g := range groups {
		n := cf.CountGroup(g)
		desc := fmt.Sprintf("%d sources", n)
		if n == 1 {
			desc = "1 source"
		}
		out = append(out, candidate{value: g, desc: desc})
	}
	return out
}

// completeSourceHandles completes saved source handles. Used for the positional
// source argument of query-shaped commands (src, ping, inspect, schema, diff)
// and the --src flag.
func completeSourceHandles(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cf, err := iqconfig.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return candidateStrings(withPrefix(handleCandidates(cf), toComplete)), cobra.ShellCompDirectiveNoFileComp
}

// completeGroups completes saved source groups (ls, group).
func completeGroups(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cf, err := iqconfig.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return candidateStrings(withPrefix(groupCandidates(cf), toComplete)), cobra.ShellCompDirectiveNoFileComp
}

// completeHandlesAndGroups completes both source handles and groups, for
// commands that accept either (rm, mv).
func completeHandlesAndGroups(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cf, err := iqconfig.Load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	all := append(handleCandidates(cf), groupCandidates(cf)...)
	return candidateStrings(withPrefix(all, toComplete)), cobra.ShellCompDirectiveNoFileComp
}

// configKeyCandidates lists the persistable option names, sharing the one
// persistableOptions allowlist with validation. The description is the usage
// text of the flag that the option aliases, so the text has one source. An
// option with no matching flag has no description.
func configKeyCandidates(cmd *cobra.Command) []candidate {
	out := plainCandidates(persistableOptions)
	if cmd == nil {
		return out
	}
	for i := range out {
		if f := cmd.Root().Flags().Lookup(out[i].value); f != nil {
			out[i].desc = f.Usage
		}
	}
	return out
}

// completeConfigKeys completes the persistable option names (config get/set).
func completeConfigKeys(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return candidateStrings(withPrefix(configKeyCandidates(cmd), toComplete)), cobra.ShellCompDirectiveNoFileComp
}

// dumpFormatCandidates lists the canonical dump format names for --from-format,
// with the tool or shape that produces each one. The list comes from the file
// driver, so it cannot drift from the formats that driver reads. The aliases
// that iqfile.ParseFormat accepts, such as json, are not offered.
func dumpFormatCandidates() []candidate {
	infos := iqfile.SupportedFormats()
	out := make([]candidate, len(infos))
	for i, fi := range infos {
		out[i] = candidate{value: fi.Name(), desc: fi.Source}
	}
	return out
}

// completeDumpFormats completes the --from-format values.
func completeDumpFormats(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return candidateStrings(withPrefix(dumpFormatCandidates(), toComplete)), cobra.ShellCompDirectiveNoFileComp
}

// Static value allowlists for the enum-valued flags. Each mirrors a validator
// that switches over the same set: parseLogLevel, validateErrorFormat, and
// numfmt.ParseDecimalMode. The validators are switches, not slices, so the
// values are restated here. A round-trip test feeds every candidate back
// through its validator.
var (
	// decimalModeNames mirrors numfmt.ParseDecimalMode (--format.decimal).
	decimalModeNames = []string{"auto", "number", "string"}
	// logLevelNames mirrors parseLogLevel (--log.level); the WARNING alias is
	// left out, since completion offers the canonical spelling only.
	logLevelNames = []string{"DEBUG", "INFO", "WARN", "ERROR"}
	// textJSONNames serves both --log.format and --error.format, whose
	// validators (newLogHandler, validateErrorFormat) take the same pair.
	textJSONNames = []string{"text", "json"}
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
	return asCompletion(fixedCandidates(values...))
}

// fixedCandidates is fixedValues before rendering, for completeCSV.
func fixedCandidates(values ...string) candidateFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]candidate, cobra.ShellCompDirective) {
		return withPrefix(plainCandidates(values), toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// completeCSV adapts a completion function to a comma-separated list flag
// (--only, --section): it completes the segment after the last comma and
// re-attaches the segments already typed, since the shell replaces the whole
// word. A value already in the list is dropped from the candidates, so a long
// selection narrows as it is built. Descriptions pass through, and the typed
// head joins the value only. ShellCompDirectiveNoSpace keeps the cursor
// put so another comma and segment can follow.
func completeCSV(fn candidateFunc) cobra.CompletionFunc {
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
		cands, _ := fn(cmd, args, tail)
		out := make([]candidate, 0, len(cands))
		for _, c := range cands {
			if !chosen[c.value] {
				out = append(out, candidate{value: head + c.value, desc: c.desc})
			}
		}
		if len(out) == 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return candidateStrings(out), cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	}
}

// completeInspectOnly completes `inspect --only` / `diff --section` with the
// subcommands of the source's own backend, resolved offline from the stored URL
// scheme. A source that cannot be resolved yields no candidates, per this file's
// best-effort contract.
func completeInspectOnly(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	subs := inspectSubcommands(sourceDriverName(cmd, args))
	return completeCSV(fixedCandidates(subs...))(cmd, args, toComplete)
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
		return candidateStrings(withPrefix(plainCandidates(optionValues(cmd, args[0])), toComplete)), cobra.ShellCompDirectiveNoFileComp
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
	return candidateStrings(withPrefix(handleCandidates(cf), toComplete)), cobra.ShellCompDirectiveDefault
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

// withPrefix returns the candidates whose value has toComplete as a prefix,
// preserving order. It never matches against a description. An empty toComplete
// keeps them all.
func withPrefix(candidates []candidate, toComplete string) []candidate {
	if toComplete == "" {
		return candidates
	}
	out := make([]candidate, 0, len(candidates))
	for _, c := range candidates {
		if strings.HasPrefix(c.value, toComplete) {
			out = append(out, c)
		}
	}
	return out
}

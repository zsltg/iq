package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flagGroupKey is the pflag annotation key under which each root flag records
// its help section. groupedFlagUsages reads it to render flags in labeled
// sections instead of one flat alphabetical list.
const flagGroupKey = "iq_flag_group"

// Help section titles. A flag with no group annotation (Cobra's auto-added
// --help/--version) falls into groupOther, which renders last.
const (
	groupSource      = "Source"
	groupQuery       = "Query"
	groupOutput      = "Output"
	groupWrite       = "Write"
	groupDisplay     = "Display"
	groupDiagnostics = "Diagnostics"
	groupOther       = "Options"
)

// flagGroupOrder fixes the section order in help output.
var flagGroupOrder = []string{
	groupSource,
	groupQuery,
	groupOutput,
	groupWrite,
	groupDisplay,
	groupDiagnostics,
	groupOther,
}

// rootFlagGroups maps each root flag name to its help section. Every persistent
// and local flag registered in newRootCmd appears here exactly once; a flag left
// out would silently land in groupOther, which the coverage-guard test rejects.
var rootFlagGroups = map[string]string{
	"src":     groupSource,
	"config":  groupSource,
	"timeout": groupSource,

	"unbounded":      groupQuery,
	"no-compile":     groupQuery,
	"no-cache":       groupQuery,
	"no-cache-index": groupQuery,
	"explain":        groupQuery,
	"from":           groupQuery,
	"combine":        groupQuery,

	"json":           groupOutput,
	"jsona":          groupOutput,
	"jsonl":          groupOutput,
	"yaml":           groupOutput,
	"raw":            groupOutput,
	"format":         groupOutput,
	"format.decimal": groupOutput,
	"compact":        groupOutput,
	"output":         groupOutput,

	"insert":       groupWrite,
	"typed":        groupWrite,
	"key":          groupWrite,
	"key-field":    groupWrite,
	"key-prefix":   groupWrite,
	"type":         groupWrite,
	"no-overwrite": groupWrite,
	"replace":      groupWrite,
	"force":        groupWrite,
	"dry-run":      groupWrite,
	"from-format":  groupWrite,

	"monochrome":  groupDisplay,
	"color":       groupDisplay,
	"no-progress": groupDisplay,

	"verbose":                   groupDiagnostics,
	"log":                       groupDiagnostics,
	"log.file":                  groupDiagnostics,
	"log.level":                 groupDiagnostics,
	"log.format":                groupDiagnostics,
	"error.format":              groupDiagnostics,
	"error.stack":               groupDiagnostics,
	"error.format.text.verbose": groupDiagnostics,
	"debug.pprof":               groupDiagnostics,
}

// annotateFlagGroups tags each root flag with its help section. It runs after
// every flag is registered. The annotation lands on the shared *pflag.Flag, so a
// persistent flag stays tagged when it surfaces as a subcommand's inherited
// flag. Persistent flags live in PersistentFlags and local ones in Flags, so we
// look each name up in the set that holds it.
func annotateFlagGroups(cmd *cobra.Command) {
	for name, group := range rootFlagGroups {
		fs := cmd.PersistentFlags()
		if fs.Lookup(name) == nil {
			fs = cmd.Flags()
		}
		// SetAnnotation errors only on an unknown flag name; every key in
		// rootFlagGroups matches a registered flag, so the error cannot fire on
		// shipped code and swallowing it keeps help rendering panic-free.
		_ = fs.SetAnnotation(name, flagGroupKey, []string{group})
	}
}

// groupedFlagUsages renders a flag set in labeled sections. It is registered as
// the "groupedFlags" template func and replaces the default
// {{.LocalFlags.FlagUsages}} / {{.InheritedFlags.FlagUsages}} in the usage
// template. A set with no grouped flag (a subcommand's own local flags) renders
// byte-for-byte as Cobra's default, so only the annotated global flags gain
// sections.
func groupedFlagUsages(fs *pflag.FlagSet) string {
	if !hasGroupedFlag(fs) {
		return fs.FlagUsages()
	}
	// Partition into a temporary FlagSet per group, keeping each flag's
	// definition so pflag renders type and default exactly as before.
	sets := map[string]*pflag.FlagSet{}
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		group := groupOther
		if vals := f.Annotations[flagGroupKey]; len(vals) > 0 {
			group = vals[0]
		}
		set, ok := sets[group]
		if !ok {
			set = pflag.NewFlagSet(group, pflag.ContinueOnError)
			sets[group] = set
		}
		set.AddFlag(f)
	})
	var b strings.Builder
	for _, group := range flagGroupOrder {
		set, ok := sets[group]
		if !ok {
			continue
		}
		usage := strings.TrimRight(set.FlagUsages(), "\n")
		if usage == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("  " + group + ":\n")
		// pflag indents flag lines by two spaces; add two more so they sit under
		// the section header.
		for _, line := range strings.Split(usage, "\n") {
			b.WriteString("  " + line + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// hasGroupedFlag reports whether any visible flag in fs carries a group
// annotation, i.e. whether grouped rendering applies at all.
func hasGroupedFlag(fs *pflag.FlagSet) bool {
	found := false
	fs.VisitAll(func(f *pflag.Flag) {
		if len(f.Annotations[flagGroupKey]) > 0 {
			found = true
		}
	})
	return found
}

// installGroupedHelp registers the groupedFlags template func and rewrites the
// command's usage template to route both the local and inherited flag blocks
// through it. Set on the root command, it propagates to every subcommand because
// Cobra's UsageTemplate walks to the parent when a command has none of its own.
func installGroupedHelp(root *cobra.Command) {
	cobra.AddTemplateFunc("groupedFlags", groupedFlagUsages)
	annotateFlagGroups(root)
	tmpl := root.UsageTemplate()
	tmpl = strings.ReplaceAll(tmpl, ".LocalFlags.FlagUsages", "groupedFlags .LocalFlags")
	tmpl = strings.ReplaceAll(tmpl, ".InheritedFlags.FlagUsages", "groupedFlags .InheritedFlags")
	root.SetUsageTemplate(tmpl)
}

// Command group IDs for the root's "Available Commands" section in --help.
// Cobra (v1.4+) renders these as labeled sections natively once a command's
// GroupID is set, so unlike the flag grouping above this needs no template
// surgery: defaultUsageTemplate already branches on len(.Groups).
const (
	cmdGroupSources = "sources"
	cmdGroupQuery   = "query"
	cmdGroupConfig  = "config"
	cmdGroupInfo    = "info"
)

// rootCommandGroupOrder fixes both the set of groups and their render order in
// --help (Cobra renders groups in root.Groups() order, i.e. AddGroup call order).
var rootCommandGroupOrder = []*cobra.Group{
	{ID: cmdGroupSources, Title: "Sources:"},
	{ID: cmdGroupQuery, Title: "Query & Data:"},
	{ID: cmdGroupConfig, Title: "Configuration:"},
	{ID: cmdGroupInfo, Title: "Info:"},
}

// rootCommandGroups maps each root subcommand's name to its help group. Every
// command added in newRootCmd's root.AddCommand call appears here exactly once;
// a command left out would silently land in Cobra's ungrouped "Additional
// Commands:" section, which the coverage-guard test rejects. Cobra's own
// auto-added help/completion commands are deliberately absent and fall through
// to "Additional Commands:".
var rootCommandGroups = map[string]string{
	"add":   cmdGroupSources,
	"ls":    cmdGroupSources,
	"rm":    cmdGroupSources,
	"mv":    cmdGroupSources,
	"src":   cmdGroupSources,
	"group": cmdGroupSources,
	"ping":  cmdGroupSources,

	"exec":    cmdGroupQuery,
	"data":    cmdGroupQuery,
	"diff":    cmdGroupQuery,
	"inspect": cmdGroupQuery,
	"schema":  cmdGroupQuery,

	"config": cmdGroupConfig,
	"cache":  cmdGroupConfig,

	"driver":  cmdGroupInfo,
	"version": cmdGroupInfo,
}

// installCommandGroups registers the root's command groups and assigns each
// already-added subcommand its GroupID, so --help renders "Available Commands"
// as labeled sections instead of one flat alphabetical list.
func installCommandGroups(root *cobra.Command) {
	root.AddGroup(rootCommandGroupOrder...)
	for _, sub := range root.Commands() {
		if id, ok := rootCommandGroups[sub.Name()]; ok {
			sub.GroupID = id
		}
	}
}

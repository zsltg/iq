package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	iqfile "github.com/zsltg/iq/drivers/file"
	iqconfig "github.com/zsltg/iq/internal/config"
)

// cacheDir returns the directory the file:// decode cache lives in:
// <user cache dir>/iq/dumps. It returns "" when the cache dir cannot be located,
// which disables caching rather than failing an otherwise-valid run — the same
// fallback the log file uses.
func cacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "iq", "dumps")
}

// fileCacheConfig builds the decode-cache policy for a file:// store from the
// invocation flags: disabled by --no-cache or when no cache dir is available,
// otherwise on with the default size floor.
func (c *config) fileCacheConfig() iqfile.CacheConfig {
	if c.noCache {
		return iqfile.CacheConfig{}
	}
	dir := cacheDir()
	if dir == "" {
		return iqfile.CacheConfig{}
	}
	return iqfile.CacheConfig{Dir: dir, Enabled: true, Index: !c.noCacheIndex}
}

// newCacheCmd builds `iq cache`: inspect and clear the file:// dump decode cache,
// mirroring sq's `cache` group. Caching is transparent — these commands only let
// a user see where it lives, what it holds, and reset it.
func newCacheCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "cache",
		Short: "Inspect and clear the file:// dump decode cache",
		Long: "Inspect and clear the decode cache for file:// dump sources. iq caches the\n" +
			"decoded, normalized records of a local dump above 4 MiB, so a later query skips\n" +
			"re-parsing the RDB/BSON/JSON. The cache keys on the dump's path, size, and mtime,\n" +
			"so editing the dump invalidates it automatically. `location` prints the cache\n" +
			"directory, `stat` lists cached dumps, and `clear` removes them. Bypass caching for\n" +
			"one run with --no-cache, or turn it off by default with `iq config set no-cache true`.",
		Args: cobra.NoArgs,
	}
	c.AddCommand(
		newCacheLocationCmd(),
		newCacheStatCmd(),
		newCacheClearCmd(),
	)
	return c
}

// newCacheLocationCmd builds `iq cache location`: print the cache directory path.
func newCacheLocationCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "location",
		Short: "Print the decode cache directory path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := cacheDir()
			if dir == "" {
				return fmt.Errorf("cannot locate the user cache directory")
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), dir)
			return err
		},
	}
}

// cacheStatRow is the JSON/YAML shape of one cached dump in `iq cache stat`.
type cacheStatRow struct {
	Dump      string `json:"dump"`
	DumpBytes int64  `json:"dump_bytes"`
	CacheFile string `json:"cache_file"`
	CacheByte int64  `json:"cache_bytes"`
}

// newCacheStatCmd builds `iq cache stat`: list the cached dumps with their sizes.
func newCacheStatCmd() *cobra.Command {
	var jsonOut, yamlOut bool
	c := &cobra.Command{
		Use:   "stat",
		Short: "List the cached dumps and their sizes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := cacheDir()
			if dir == "" {
				return fmt.Errorf("cannot locate the user cache directory")
			}
			return cacheStat(cmd.OutOrStdout(), dir, jsonOut, yamlOut)
		},
	}
	c.Flags().BoolVarP(&jsonOut, "json", "j", false, "emit machine-readable JSON")
	c.Flags().BoolVarP(&yamlOut, "yaml", "y", false, "emit machine-readable YAML")
	c.MarkFlagsMutuallyExclusive("json", "yaml")
	return c
}

// cacheStat renders the cache entries in dir as a table (with a total) or, with
// jsonOut/yamlOut, as machine-readable output. An empty cache prints a header
// row only (table) or an empty list (structured), so the output is well-formed
// either way.
func cacheStat(out io.Writer, dir string, jsonOut, yamlOut bool) error {
	entries, err := iqfile.ListCache(dir)
	if err != nil {
		return err
	}
	if jsonOut || yamlOut {
		rows := make([]cacheStatRow, 0, len(entries))
		for _, e := range entries {
			rows = append(rows, cacheStatRow{Dump: e.DumpPath, DumpBytes: e.DumpSize, CacheFile: e.File, CacheByte: e.CacheSize})
		}
		return writeStructured(out, rows, yamlOut)
	}
	rows := [][]tableCell{{
		coloredCell("DUMP", pal.header),
		coloredCell("DUMP SIZE", pal.header),
		coloredCell("CACHE SIZE", pal.header),
	}}
	var total int64
	for _, e := range entries {
		total += e.CacheSize
		rows = append(rows, []tableCell{
			cell(e.DumpPath),
			cell(humanBytes(e.DumpSize)),
			cell(humanBytes(e.CacheSize)),
		})
	}
	if len(entries) > 0 {
		rows = append(rows, []tableCell{cell("TOTAL"), cell(""), cell(humanBytes(total))})
	}
	return renderTable(out, rows)
}

// newCacheClearCmd builds `iq cache clear [source|path]`: remove all cached
// dumps, or only the one for a named source (file:// only) or dump path.
func newCacheClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear [source|path]",
		Short: "Remove cached dumps (all, or one source/path)",
		Long: "Remove decode-cache files. With no argument, clear the whole cache; with a saved\n" +
			"file:// source name or a dump file path, clear only that dump's cache entry.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := cacheDir()
			if dir == "" {
				return fmt.Errorf("cannot locate the user cache directory")
			}
			target := ""
			if len(args) == 1 {
				target = dumpPathArg(args[0])
			}
			n, err := iqfile.RemoveCache(dir, target)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "removed %d cache file(s)\n", n)
			return err
		},
	}
}

// dumpPathArg maps a `cache clear` argument to a dump path: a saved file:// source
// resolves to its dump path; anything else is taken as a filesystem path.
func dumpPathArg(arg string) string {
	if cf, err := iqconfig.Load(); err == nil {
		if src, _, ok := cf.Resolve(arg); ok && schemeOf(src.URL) == "file" {
			if p, err := iqfile.DumpPath(src.URL); err == nil {
				return p
			}
		}
	}
	return arg
}

// humanBytes renders a byte count as a compact binary-unit string (e.g. 4.0 MiB),
// so a cache listing is readable at a glance rather than a raw byte count.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

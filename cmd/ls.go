package cmd

import (
	"fmt"
	"io"
	"strings"

	iqfile "github.com/zsltg/iq/drivers/file"
	iqmongo "github.com/zsltg/iq/drivers/mongo"
	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/secret"
)

// sourceRow is the JSON shape of one source in `iq ls --json`. Location is
// redacted unless --reveal (inline passwords) or --expand (keyring passwords) is
// set, so a stored password never leaks by default. Format and Options are the
// verbose-only detail: Format is a file source's detected dump format, Options
// its stored per-source flag defaults.
type sourceRow struct {
	Handle     string            `json:"handle"`
	Driver     string            `json:"driver"`
	Location   string            `json:"location"`
	Collection string            `json:"collection,omitempty"`
	Keyring    bool              `json:"keyring,omitempty"`
	Active     bool              `json:"active,omitempty"`
	Format     string            `json:"format,omitempty"`
	Options    map[string]string `json:"options,omitempty"`
}

// groupRow is the JSON shape of one group in `iq ls -g --json`.
type groupRow struct {
	Group   string `json:"group"`
	Sources int    `json:"sources"`
}

// lsOptions holds the options of one `iq ls` listing: the group filter, the
// detail and password flags, and the output format.
type lsOptions struct {
	filter                  string
	verbose, reveal, expand bool
	jsonOut, yamlOut        bool
}

// listSources renders the sources, optionally limited to a group, and marks the
// active one. The reveal flag un-redacts inline passwords, and the expand flag
// resolves keyring passwords. The verbose flag adds a driver column. The
// jsonOut and yamlOut flags emit machine-readable output.
func listSources(out io.Writer, cf *iqconfig.Config, opts lsOptions) error {
	list := inGroup(cf.List(), opts.filter)

	if opts.jsonOut || opts.yamlOut {
		return writeStructured(out, sourceRows(cf, list, opts), opts.yamlOut)
	}

	if len(list) == 0 {
		return writeNoSources(out, opts.filter)
	}
	if cf.Group != "" {
		if _, err := fmt.Fprintf(out, "active group: %s\n", cf.Group); err != nil {
			return err
		}
	}
	return renderTable(out, sourceTable(cf, list, opts))
}

// inGroup keeps the sources of a group. A name matches when it equals the group
// or starts with the group and a slash. An empty group keeps every source.
func inGroup(list []iqconfig.Handle, group string) []iqconfig.Handle {
	if group == "" {
		return list
	}
	kept := list[:0:0]
	for _, h := range list {
		if h.Name == group || strings.HasPrefix(h.Name, group+"/") {
			kept = append(kept, h)
		}
	}
	return kept
}

// writeNoSources writes the message for an empty listing, for a group or for
// the whole config.
func writeNoSources(out io.Writer, group string) error {
	if group != "" {
		_, err := fmt.Fprintf(out, "no sources in group %s\n", group)
		return err
	}
	_, err := fmt.Fprintln(out, "no sources; add one with `iq add <uri>` (name it with -n)")
	return err
}

// sourceTable builds the rows of the source table. With verbose it opens with a
// header row.
func sourceTable(cf *iqconfig.Config, list []iqconfig.Handle, opts lsOptions) [][]tableCell {
	rows := make([][]tableCell, 0, len(list)+1)
	if opts.verbose {
		rows = append(rows, []tableCell{
			coloredCell("HANDLE", pal.header),
			coloredCell("DRIVER", pal.header),
			coloredCell("LOCATION", pal.header),
			coloredCell("FORMAT", pal.header),
			coloredCell("OPTIONS", pal.header),
		})
	}
	for _, h := range list {
		rows = append(rows, sourceCells(keyringFor(cf), h, h.Name == cf.Active, opts))
	}
	return rows
}

// sourceCells builds the cells of one source row: the marker and handle, the
// driver, the location, and with verbose the format and the options.
func sourceCells(kr secret.Keyring, h iqconfig.Handle, active bool, opts lsOptions) []tableCell {
	marker := " "
	nameColor := pal.handle
	if active {
		marker = "*"
		nameColor = pal.active
	}
	name := coloredCell(marker+" "+h.Name, nameColor)

	// The default collection already rides in the shown URL as ?collection=,
	// so it is not repeated as a separate tag here.
	loc := displayLocation(kr, h.Source, h.Name, locationFlags{reveal: opts.reveal, expand: opts.expand})
	driver := driverName(h.Source.URL)
	if !opts.verbose {
		return []tableCell{name, coloredCell(driver, pal.faint), coloredCell(loc, pal.location)}
	}
	loc += keyringTag(h.Source.Keyring)
	return []tableCell{
		name,
		coloredCell(driver, pal.faint),
		coloredCell(loc, pal.location),
		formatCell(driver, h.Source.URL),
		optionsCell(h.Source.Options),
	}
}

// sourceRows projects saved sources into the machine-readable row shape shared by
// `iq ls --json` and the MCP iq_sources tool. Every location is redacted unless
// reveal (inline passwords) or expand (keyring passwords) is set, so a stored
// password never leaves the process by default.
func sourceRows(cf *iqconfig.Config, list []iqconfig.Handle, opts lsOptions) []sourceRow {
	kr := keyringFor(cf)
	rows := make([]sourceRow, 0, len(list))
	for _, h := range list {
		driver := driverName(h.Source.URL)
		row := sourceRow{
			Handle:     h.Name,
			Driver:     driver,
			Location:   displayLocation(kr, h.Source, h.Name, locationFlags{reveal: opts.reveal, expand: opts.expand}),
			Collection: iqmongo.CollectionFromURI(h.Source.URL),
			Keyring:    h.Source.Keyring,
			Active:     h.Name == cf.Active,
		}
		if opts.verbose {
			row.Options = h.Source.Options
			row.Format = fileFormat(driver, h.Source.URL)
		}
		rows = append(rows, row)
	}
	return rows
}

// fileFormat returns the detected dump format of a file source. It returns ""
// for a source of another driver, or when the detection fails.
func fileFormat(driver, url string) string {
	if driver != "file" {
		return ""
	}
	f, err := iqfile.DetectFormat(url)
	if err != nil {
		return ""
	}
	return f.String()
}

// formatCell renders the FORMAT column of a source for `iq ls -v`. A file
// source shows its detected dump format. A non-file source shows an em dash. A
// file whose format cannot be detected (a moved or unreadable dump) shows a
// faint "?". One bad source never fails the whole listing.
func formatCell(driver, url string) tableCell {
	if driver != "file" {
		return coloredCell("—", pal.faint)
	}
	f, err := iqfile.DetectFormat(url)
	if err != nil {
		return coloredCell("?", pal.faint)
	}
	return coloredCell(f.String(), pal.change)
}

// optionsCell renders the OPTIONS column of a source, faint. It renders an
// uncolored empty cell when the source stores none, so color mode emits no
// stray escapes for it.
func optionsCell(opts map[string]string) tableCell {
	s := renderSourceOptions(opts)
	if s == "" {
		return cell("")
	}
	return coloredCell(s, pal.faint)
}

// renderSourceOptions renders a source's stored options as space-joined key=value
// pairs in persistableOptions order (the order `iq config ls` uses), or "" when
// the source has none.
func renderSourceOptions(opts map[string]string) string {
	if len(opts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, k := range persistableOptions {
		v, ok := opts[k]
		if !ok {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k + "=" + v)
	}
	return b.String()
}

// listGroups renders the distinct groups. verbose adds a source count.
// jsonOut and yamlOut emit machine-readable output.
func listGroups(out io.Writer, cf *iqconfig.Config, opts lsOptions) error {
	groups := cf.Groups()
	if opts.jsonOut || opts.yamlOut {
		return writeStructured(out, groupJSON(cf, groups), opts.yamlOut)
	}
	if len(groups) == 0 {
		_, err := fmt.Fprintln(out, "no groups; group a source by naming it group/name")
		return err
	}
	rows := make([][]tableCell, 0, len(groups))
	for _, g := range groups {
		rows = append(rows, groupCells(g, g == cf.Group, cf, opts.verbose))
	}
	return renderTable(out, rows)
}

// groupCells builds the cells of one group row: the marker and name, and with
// verbose the source count.
func groupCells(g string, active bool, cf *iqconfig.Config, verbose bool) []tableCell {
	marker := " "
	if active {
		marker = "*"
	}
	name := tableCell{text: marker + " " + g}
	if active {
		name.c = pal.active
	}
	if verbose {
		return []tableCell{name, cell(fmt.Sprintf("%d sources", cf.CountGroup(g)))}
	}
	return []tableCell{name}
}

// groupJSON projects the groups into the machine-readable row shape.
func groupJSON(cf *iqconfig.Config, groups []string) []groupRow {
	rows := make([]groupRow, 0, len(groups))
	for _, g := range groups {
		rows = append(rows, groupRow{Group: g, Sources: cf.CountGroup(g)})
	}
	return rows
}

// locationFlags are the display toggles of displayLocation.
type locationFlags struct {
	reveal, expand bool
}

// displayLocation returns the URI of a source for display, redacted by default
// so a stored password never prints. The expand flag resolves a keyring-backed
// password and inlines it. If the lookup fails, it keeps the stored
// password-less URI and returns no error. The reveal flag prints the result
// verbatim. Otherwise any password is redacted to xxxxx. So reveal un-redacts
// inline passwords, and expand resolves keyring ones. This mirrors the
// --reveal/--expand split of sq.
func displayLocation(kr secret.Keyring, src iqconfig.Source, handle string, flags locationFlags) string {
	raw := src.URL
	if flags.expand {
		if u, err := effectiveURL(kr, src, handle); err == nil {
			raw = u
		}
	}
	if !flags.reveal {
		return redactURL(raw)
	}
	return raw
}

// keyringTag returns a trailing " [keyring]" marker for a keyring-backed source,
// or "" otherwise.
func keyringTag(keyring bool) string {
	if keyring {
		return " [keyring]"
	}
	return ""
}

package cmd

import (
	"fmt"
	"io"
	"strings"

	iqfile "github.com/zsltg/iq/drivers/file"
	iqmongo "github.com/zsltg/iq/drivers/mongo"
	iqconfig "github.com/zsltg/iq/internal/config"
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

// listSources renders the sources, optionally limited to a group, marking the
// active one. reveal un-redacts inline passwords and expand resolves keyring
// passwords; verbose adds a driver column; jsonOut/yamlOut emit machine-readable
// output.
func listSources(out io.Writer, cf *iqconfig.Config, filter string, verbose, reveal, jsonOut, yamlOut, expand bool) error {
	list := cf.List()
	if filter != "" {
		kept := list[:0:0]
		for _, h := range list {
			if h.Name == filter || strings.HasPrefix(h.Name, filter+"/") {
				kept = append(kept, h)
			}
		}
		list = kept
	}

	if jsonOut || yamlOut {
		return writeStructured(out, sourceRows(cf, list, verbose, reveal, expand), yamlOut)
	}

	if len(list) == 0 {
		if filter != "" {
			_, err := fmt.Fprintf(out, "no sources in group %s\n", filter)
			return err
		}
		_, err := fmt.Fprintln(out, "no sources; add one with `iq add <uri>` (name it with -n)")
		return err
	}
	if cf.Group != "" {
		if _, err := fmt.Fprintf(out, "active group: %s\n", cf.Group); err != nil {
			return err
		}
	}
	rows := make([][]tableCell, 0, len(list)+1)
	if verbose {
		rows = append(rows, []tableCell{
			coloredCell("HANDLE", pal.header),
			coloredCell("DRIVER", pal.header),
			coloredCell("LOCATION", pal.header),
			coloredCell("FORMAT", pal.header),
			coloredCell("OPTIONS", pal.header),
		})
	}
	for _, h := range list {
		active := h.Name == cf.Active
		marker := " "
		nameColor := pal.handle
		if active {
			marker = "*"
			nameColor = pal.active
		}
		name := coloredCell(marker+" "+h.Name, nameColor)

		// The default collection already rides in the shown URL as ?collection=,
		// so it is not repeated as a separate tag here.
		loc := displayLocation(h.Source, h.Name, reveal, expand)
		driver := driverName(h.Source.URL)
		if !verbose {
			rows = append(rows, []tableCell{name, coloredCell(driver, pal.faint), coloredCell(loc, pal.location)})
			continue
		}
		loc += keyringTag(h.Source.Keyring)
		rows = append(rows, []tableCell{
			name,
			coloredCell(driver, pal.faint),
			coloredCell(loc, pal.location),
			formatCell(driver, h.Source.URL),
			optionsCell(h.Source.Options),
		})
	}
	return renderTable(out, rows)
}

// sourceRows projects saved sources into the machine-readable row shape shared by
// `iq ls --json` and the MCP iq_sources tool. Every location is redacted unless
// reveal (inline passwords) or expand (keyring passwords) is set, so a stored
// password never leaves the process by default.
func sourceRows(cf *iqconfig.Config, list []iqconfig.Handle, verbose, reveal, expand bool) []sourceRow {
	rows := make([]sourceRow, 0, len(list))
	for _, h := range list {
		driver := driverName(h.Source.URL)
		row := sourceRow{
			Handle:     h.Name,
			Driver:     driver,
			Location:   displayLocation(h.Source, h.Name, reveal, expand),
			Collection: iqmongo.CollectionFromURI(h.Source.URL),
			Keyring:    h.Source.Keyring,
			Active:     h.Name == cf.Active,
		}
		if verbose {
			row.Options = h.Source.Options
			if driver == "file" {
				if f, err := iqfile.DetectFormat(h.Source.URL); err == nil {
					row.Format = f.String()
				}
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// formatCell renders a source's FORMAT column for `iq ls -v`: a file source's
// detected dump format, an em dash for a non-file source, or a faint "?" when a
// file's format cannot be detected (a moved or unreadable dump), so one bad
// source never fails the whole listing.
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

// optionsCell renders a source's OPTIONS column, faint, or an uncolored empty
// cell when the source stores none (so color mode emits no stray escapes for it).
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

// listGroups renders the distinct groups. verbose adds a source count;
// jsonOut/yamlOut emit machine-readable output.
func listGroups(out io.Writer, cf *iqconfig.Config, verbose, jsonOut, yamlOut bool) error {
	groups := cf.Groups()
	if jsonOut || yamlOut {
		rows := make([]groupRow, 0, len(groups))
		for _, g := range groups {
			rows = append(rows, groupRow{Group: g, Sources: cf.CountGroup(g)})
		}
		return writeStructured(out, rows, yamlOut)
	}
	if len(groups) == 0 {
		_, err := fmt.Fprintln(out, "no groups; group a source by naming it group/name")
		return err
	}
	rows := make([][]tableCell, 0, len(groups))
	for _, g := range groups {
		marker := " "
		active := g == cf.Group
		if active {
			marker = "*"
		}
		name := tableCell{text: marker + " " + g}
		if active {
			name.c = pal.active
		}
		if verbose {
			rows = append(rows, []tableCell{name, cell(fmt.Sprintf("%d sources", cf.CountGroup(g)))})
		} else {
			rows = append(rows, []tableCell{name})
		}
	}
	return renderTable(out, rows)
}

// displayLocation returns a source's URL for display, redacted by default so a
// stored password never prints. expand resolves a keyring-backed password and
// inlines it (on lookup failure it keeps the stored password-less URL rather than
// error); reveal then prints the result verbatim, otherwise any password is
// redacted to xxxxx. reveal thus un-redacts inline passwords, expand resolves
// keyring ones, mirroring sq's --reveal/--expand split.
func displayLocation(src iqconfig.Source, handle string, reveal, expand bool) string {
	raw := src.URL
	if expand && src.Keyring {
		if u, err := effectiveURL(src, handle); err == nil {
			raw = u
		}
	}
	if !reveal {
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

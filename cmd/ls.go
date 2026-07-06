package cmd

import (
	"fmt"
	"io"
	"strings"

	iqmongo "github.com/zsltg/iq/drivers/mongo"
	iqconfig "github.com/zsltg/iq/internal/config"
)

// sourceRow is the JSON shape of one source in `iq ls --json`. Location is
// redacted unless --reveal (inline passwords) or --expand (keyring passwords) is
// set, so a stored password never leaks by default.
type sourceRow struct {
	Handle     string `json:"handle"`
	Driver     string `json:"driver"`
	Location   string `json:"location"`
	Collection string `json:"collection,omitempty"`
	Keyring    bool   `json:"keyring,omitempty"`
	Active     bool   `json:"active,omitempty"`
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
		rows := make([]sourceRow, 0, len(list))
		for _, h := range list {
			rows = append(rows, sourceRow{
				Handle:     h.Name,
				Driver:     driverName(h.Source.URL),
				Location:   displayLocation(h.Source, h.Name, reveal, expand),
				Collection: iqmongo.CollectionFromURI(h.Source.URL),
				Keyring:    h.Source.Keyring,
				Active:     h.Name == cf.Active,
			})
		}
		return writeStructured(out, rows, yamlOut)
	}

	if len(list) == 0 {
		if filter != "" {
			_, err := fmt.Fprintf(out, "no sources in group %s\n", filter)
			return err
		}
		_, err := fmt.Fprintln(out, "no sources; add one with `iq add <name> <url>`")
		return err
	}
	if cf.Group != "" {
		if _, err := fmt.Fprintf(out, "active group: %s\n", cf.Group); err != nil {
			return err
		}
	}
	rows := make([][]tableCell, 0, len(list))
	for _, h := range list {
		marker := " "
		active := h.Name == cf.Active
		if active {
			marker = "*"
		}
		coll := ""
		if c := iqmongo.CollectionFromURI(h.Source.URL); c != "" {
			coll = "(" + c + ")"
		}
		name := tableCell{text: marker + " " + h.Name}
		if active {
			name.c = pal.active
		}
		if verbose {
			rows = append(rows, []tableCell{name, cell(driverName(h.Source.URL)), cell(displayLocation(h.Source, h.Name, reveal, expand)), cell(coll + keyringTag(h.Source.Keyring))})
		} else {
			rows = append(rows, []tableCell{name, cell(displayLocation(h.Source, h.Name, reveal, expand)), cell(coll)})
		}
	}
	return renderTable(out, rows)
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

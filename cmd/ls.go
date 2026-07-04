package cmd

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// sourceRow is the JSON shape of one source in `iq ls --json`. Location is
// redacted unless --reveal is set, so a stored password never leaks by default.
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
// active one. reveal prints unredacted URLs; verbose adds a driver column; json
// emits machine-readable output.
func listSources(out io.Writer, cf *iqconfig.Config, filter string, verbose, jsonOut, reveal bool) error {
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

	if jsonOut {
		rows := make([]sourceRow, 0, len(list))
		for _, h := range list {
			rows = append(rows, sourceRow{
				Handle:     h.Name,
				Driver:     schemeOf(h.Source.URL),
				Location:   sourceLocation(h, reveal),
				Collection: h.Source.Collection,
				Keyring:    h.Source.Keyring,
				Active:     h.Name == cf.Active,
			})
		}
		return newJSONEncoder(out, true).Encode(rows)
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
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, h := range list {
		marker := " "
		if h.Name == cf.Active {
			marker = "*"
		}
		coll := ""
		if h.Source.Collection != "" {
			coll = "(" + h.Source.Collection + ")"
		}
		var err error
		if verbose {
			_, err = fmt.Fprintf(w, "%s %s\t%s\t%s\t%s%s\n",
				marker, h.Name, schemeOf(h.Source.URL), sourceLocation(h, reveal), coll, keyringTag(h.Source.Keyring))
		} else {
			_, err = fmt.Fprintf(w, "%s %s\t%s\t%s\n",
				marker, h.Name, sourceLocation(h, reveal), coll)
		}
		if err != nil {
			return err
		}
	}
	return w.Flush()
}

// listGroups renders the distinct groups. verbose adds a source count; json emits
// machine-readable output.
func listGroups(out io.Writer, cf *iqconfig.Config, verbose, jsonOut bool) error {
	groups := cf.Groups()
	if jsonOut {
		rows := make([]groupRow, 0, len(groups))
		for _, g := range groups {
			rows = append(rows, groupRow{Group: g, Sources: cf.CountGroup(g)})
		}
		return newJSONEncoder(out, true).Encode(rows)
	}
	if len(groups) == 0 {
		_, err := fmt.Fprintln(out, "no groups; group a source by naming it group/name")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, g := range groups {
		marker := " "
		if g == cf.Group {
			marker = "*"
		}
		var err error
		if verbose {
			_, err = fmt.Fprintf(w, "%s %s\t%d sources\n", marker, g, cf.CountGroup(g))
		} else {
			_, err = fmt.Fprintf(w, "%s %s\n", marker, g)
		}
		if err != nil {
			return err
		}
	}
	return w.Flush()
}

// sourceLocation returns a source's URL for display: redacted by default so a
// stored password never prints. When reveal is set it returns the full URL, and
// for a keyring-backed source it splices the stored password back in; if that
// lookup fails it falls back to the stored (password-less) URL rather than error.
func sourceLocation(h iqconfig.Handle, reveal bool) string {
	if !reveal {
		return redactURL(h.Source.URL)
	}
	if h.Source.Keyring {
		if u, err := effectiveURL(h.Source, h.Name); err == nil {
			return u
		}
	}
	return h.Source.URL
}

// keyringTag returns a trailing " [keyring]" marker for a keyring-backed source,
// or "" otherwise.
func keyringTag(keyring bool) string {
	if keyring {
		return " [keyring]"
	}
	return ""
}

// Package numfmt holds the presentation choices for backend numbers that the CLI
// resolves once and the driver adapters honour when normalizing values. It is an
// adapter-level utility: the query core never imports it, so the core stays free
// of any output-format concern.
package numfmt

import (
	"fmt"
	"strings"
)

// DecimalMode chooses how a non-integer decimal value from a backend is presented
// to the jq filter (and therefore to output). Because iq runs jq client-side, the
// choice is made at normalization time — it changes what the filter computes on,
// not just the printed form.
type DecimalMode int

const (
	// DecimalAuto keeps each backend's native, faithful form: MongoDB Decimal128
	// stays an exact string, a Redis fractional number stays a float64. It is the
	// zero value, so a store opened without an explicit mode defaults to it.
	DecimalAuto DecimalMode = iota
	// DecimalNumber renders decimals as bare jq numbers (float64), convenient for
	// arithmetic but lossy beyond float64's range.
	DecimalNumber
	// DecimalString renders decimals as jq strings holding the exact literal,
	// precision-safe; a filter uses tonumber to compute.
	DecimalString
)

// ParseDecimalMode resolves the --format.decimal flag value to a DecimalMode,
// case-insensitively, returning an error that lists the valid values for anything
// else so a bad flag fails fast at the CLI boundary.
func ParseDecimalMode(s string) (DecimalMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto":
		return DecimalAuto, nil
	case "number":
		return DecimalNumber, nil
	case "string":
		return DecimalString, nil
	default:
		return DecimalAuto, fmt.Errorf("invalid --format.decimal %q: want auto, number, or string", s)
	}
}

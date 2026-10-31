package pushdown_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/pushdown"
)

func TestHidesDecodedNumber(t *testing.T) {
	path := []string{"price"}
	tests := []struct {
		name string
		node predicate.Node
		mode numfmt.DecimalMode
		want bool
	}{
		{"eq number text, string mode", predicate.Eq{Path: path, Value: "1.5"}, numfmt.DecimalString, true},
		{"eq number text, auto mode", predicate.Eq{Path: path, Value: "1.5"}, numfmt.DecimalAuto, false},
		{"eq out of range text, auto mode", predicate.Eq{Path: path, Value: "2e308"}, numfmt.DecimalAuto, true},
		{"eq plain text, string mode", predicate.Eq{Path: path, Value: "apple"}, numfmt.DecimalString, false},
		{"eq integer text, string mode", predicate.Eq{Path: path, Value: "7"}, numfmt.DecimalString, false},
		{"eq number, string mode", predicate.Eq{Path: path, Value: 1.5}, numfmt.DecimalString, false},
		{"eq null, string mode", predicate.Eq{Path: path}, numfmt.DecimalString, false},
		{"gt string, string mode", predicate.Cmp{Path: path, Op: predicate.Gt, Value: "1"}, numfmt.DecimalString, true},
		{"ge number, string mode", predicate.Cmp{Path: path, Op: predicate.Ge, Value: 1.0}, numfmt.DecimalString, true},
		{"gt number, auto mode", predicate.Cmp{Path: path, Op: predicate.Gt, Value: 1.0}, numfmt.DecimalAuto, false},
		{"lt string, string mode", predicate.Cmp{Path: path, Op: predicate.Lt, Value: "1"}, numfmt.DecimalString, false},
		{"le number, string mode", predicate.Cmp{Path: path, Op: predicate.Le, Value: 1.0}, numfmt.DecimalString, false},
		{"regex, string mode", predicate.Regex{Path: path, Pattern: "^1"}, numfmt.DecimalString, true},
		{"regex, auto mode", predicate.Regex{Path: path, Pattern: "^1"}, numfmt.DecimalAuto, false},
		{"exists, string mode", predicate.Exists{Path: path}, numfmt.DecimalString, false},
		{"nil, string mode", nil, numfmt.DecimalString, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, pushdown.HidesDecodedNumber(tt.node, tt.mode))
		})
	}
}

func TestHidesDecimalNumber(t *testing.T) {
	path := []string{"price"}
	tests := []struct {
		name string
		node predicate.Node
		mode numfmt.DecimalMode
		want bool
	}{
		{"eq number text, auto mode", predicate.Eq{Path: path, Value: "9.99"}, numfmt.DecimalAuto, true},
		{"eq number text, string mode", predicate.Eq{Path: path, Value: "9.99"}, numfmt.DecimalString, true},
		{"eq number text, number mode", predicate.Eq{Path: path, Value: "9.99"}, numfmt.DecimalNumber, false},
		{"eq integer text, auto mode", predicate.Eq{Path: path, Value: "100"}, numfmt.DecimalAuto, true},
		{"eq exponent text, auto mode", predicate.Eq{Path: path, Value: "1E+3"}, numfmt.DecimalAuto, true},
		{"eq negative text, auto mode", predicate.Eq{Path: path, Value: "-0.5"}, numfmt.DecimalAuto, true},
		{"eq NaN text, auto mode", predicate.Eq{Path: path, Value: "NaN"}, numfmt.DecimalAuto, true},
		{"eq Infinity text, string mode", predicate.Eq{Path: path, Value: "Infinity"}, numfmt.DecimalString, true},
		{"eq -Infinity text, auto mode", predicate.Eq{Path: path, Value: "-Infinity"}, numfmt.DecimalAuto, true},
		{"eq NaN text, number mode", predicate.Eq{Path: path, Value: "NaN"}, numfmt.DecimalNumber, false},
		{"eq plus sign text, auto mode", predicate.Eq{Path: path, Value: "+1"}, numfmt.DecimalAuto, false},
		{"eq number with trailing text, auto mode", predicate.Eq{Path: path, Value: "1x"}, numfmt.DecimalAuto, false},
		{"eq plain text, auto mode", predicate.Eq{Path: path, Value: "apple"}, numfmt.DecimalAuto, false},
		{"eq empty text, auto mode", predicate.Eq{Path: path, Value: ""}, numfmt.DecimalAuto, false},
		{"eq number, auto mode", predicate.Eq{Path: path, Value: 9.99}, numfmt.DecimalAuto, false},
		{"eq null, auto mode", predicate.Eq{Path: path}, numfmt.DecimalAuto, false},
		{"gt number, auto mode", predicate.Cmp{Path: path, Op: predicate.Gt, Value: 1000.0}, numfmt.DecimalAuto, true},
		{"ge string, string mode", predicate.Cmp{Path: path, Op: predicate.Ge, Value: "a"}, numfmt.DecimalString, true},
		{"gt number, number mode", predicate.Cmp{Path: path, Op: predicate.Gt, Value: 1000.0}, numfmt.DecimalNumber, false},
		{"lt number, auto mode", predicate.Cmp{Path: path, Op: predicate.Lt, Value: 1000.0}, numfmt.DecimalAuto, false},
		{"le number, string mode", predicate.Cmp{Path: path, Op: predicate.Le, Value: 1000.0}, numfmt.DecimalString, false},
		{"regex, auto mode", predicate.Regex{Path: path, Pattern: "^9"}, numfmt.DecimalAuto, true},
		{"regex, number mode", predicate.Regex{Path: path, Pattern: "^9"}, numfmt.DecimalNumber, false},
		{"exists, auto mode", predicate.Exists{Path: path}, numfmt.DecimalAuto, false},
		{"nil, auto mode", nil, numfmt.DecimalAuto, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, pushdown.HidesDecimalNumber(tt.node, tt.mode))
		})
	}
}

func TestHidesDecimal(t *testing.T) {
	path := []string{"price"}
	tests := []struct {
		name string
		node predicate.Node
		mode numfmt.DecimalMode
		want bool
	}{
		{"eq number, number mode", predicate.Eq{Path: path, Value: 9.99}, numfmt.DecimalNumber, true},
		{"eq int, number mode", predicate.Eq{Path: path, Value: 10}, numfmt.DecimalNumber, true},
		{"gt number, number mode", predicate.Cmp{Path: path, Op: predicate.Gt, Value: 9.99}, numfmt.DecimalNumber, true},
		{"ge number, number mode", predicate.Cmp{Path: path, Op: predicate.Ge, Value: 9.99}, numfmt.DecimalNumber, true},
		{"lt number, number mode", predicate.Cmp{Path: path, Op: predicate.Lt, Value: 10.0}, numfmt.DecimalNumber, true},
		{"le number, number mode", predicate.Cmp{Path: path, Op: predicate.Le, Value: 10.0}, numfmt.DecimalNumber, true},
		{"eq string, number mode", predicate.Eq{Path: path, Value: "apple"}, numfmt.DecimalNumber, false},
		{"eq number text, number mode", predicate.Eq{Path: path, Value: "9.99"}, numfmt.DecimalNumber, false},
		{"eq null, number mode", predicate.Eq{Path: path}, numfmt.DecimalNumber, false},
		{"eq bool, number mode", predicate.Eq{Path: path, Value: true}, numfmt.DecimalNumber, false},
		{"gt string, number mode", predicate.Cmp{Path: path, Op: predicate.Gt, Value: "a"}, numfmt.DecimalNumber, false},
		{"regex, number mode", predicate.Regex{Path: path, Pattern: "^9"}, numfmt.DecimalNumber, false},
		{"exists, number mode", predicate.Exists{Path: path}, numfmt.DecimalNumber, false},
		{"eq number, auto mode", predicate.Eq{Path: path, Value: 9.99}, numfmt.DecimalAuto, false},
		{"eq number, string mode", predicate.Eq{Path: path, Value: 9.99}, numfmt.DecimalString, false},
		{"lt number, auto mode", predicate.Cmp{Path: path, Op: predicate.Lt, Value: 10.0}, numfmt.DecimalAuto, false},
		{"gt number, auto mode", predicate.Cmp{Path: path, Op: predicate.Gt, Value: 10.0}, numfmt.DecimalAuto, true},
		{"eq number text, auto mode", predicate.Eq{Path: path, Value: "9.99"}, numfmt.DecimalAuto, true},
		{"regex, string mode", predicate.Regex{Path: path, Pattern: "^9"}, numfmt.DecimalString, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, pushdown.HidesDecimal(tt.node, tt.mode))
		})
	}
}

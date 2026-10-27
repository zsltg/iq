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

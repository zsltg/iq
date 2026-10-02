package jqfmt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatSourceWithWrongArgumentCount(t *testing.T) {
	// Only a source() call with exactly two string literals gets the nested layout.
	// Any other argument count prints as an ordinary function call.
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"one argument", `source("a")`, `source("a")`},
		{"three arguments", `source("a"; ".b"; ".c")`, `source("a"; ".b"; ".c")`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Format(tt.src, false)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFormatBindingSpacing(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"single pattern", `.a as $x | $x`, ".a as $x\n| $x"},
		{"alternative patterns", `.[] as [$a] ?// $a | $a`, ".[] as [$a] ?// $a\n| $a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Format(tt.src, false)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

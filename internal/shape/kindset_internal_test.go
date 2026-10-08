package shape

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestKindSetCollapsedIsAscending pins the order that collapsed returns. The set
// is a map, so a missing sort returns the kinds in a random order. Eight kinds
// make an accidental ascending order rare, and many passes make it negligible.
func TestKindSetCollapsedIsAscending(t *testing.T) {
	tests := []struct {
		name string
		in   []kind
		want []kind
	}{
		{
			name: "integer folds into number",
			in:   []kind{kindUnknown, kindMap, kindObject, kindArray, kindString, kindNumber, kindInteger, kindBool, kindNull},
			want: []kind{kindNull, kindBool, kindNumber, kindString, kindArray, kindObject, kindMap, kindUnknown},
		},
		{
			name: "integer stays without number",
			in:   []kind{kindUnknown, kindMap, kindObject, kindArray, kindString, kindInteger, kindBool, kindNull},
			want: []kind{kindNull, kindBool, kindInteger, kindString, kindArray, kindObject, kindMap, kindUnknown},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for pass := range 100 {
				s := kindSet{}
				for _, k := range tt.in {
					s.add(k)
				}
				require.Equal(t, tt.want, s.collapsed(), "pass %d", pass)
			}
		})
	}
}

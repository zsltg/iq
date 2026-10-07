package cassandra

import (
	"maps"
	"slices"
	"testing"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/stretchr/testify/require"
)

// The three lookup tables are read only. These tests make sure that each entry has a
// row in a table test above, so a changed or dropped entry fails a test.

func TestKeyParsersHaveARowEach(t *testing.T) {
	var covered []gocql.Type
	for _, tt := range keyTypeCases {
		if tt.wantErr != "" {
			covered = append(covered, tt.typ)
		}
	}
	slices.Sort(covered)
	covered = slices.Compact(covered)
	got := slices.Sorted(maps.Keys(keyParsers))
	require.Equal(t, covered, got)
}

func TestScalarBindersHaveARowEach(t *testing.T) {
	var covered []gocql.Type
	for _, tt := range scalarBindCases {
		covered = append(covered, tt.typ)
	}
	slices.Sort(covered)
	covered = slices.Compact(covered)
	got := slices.Sorted(maps.Keys(scalarBinders))
	require.Equal(t, covered, got)
}

package pushdown_test

import (
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/pushdown"
)

func TestCompileNegatedAnyOverExactOr(t *testing.T) {
	tests := []struct {
		name   string
		filter string
		want   predicate.Node
	}{
		{
			// An or of equalities is exact, so a negated any() over it pushes as NoneMatch.
			"negated any over an or of equalities",
			".[] | select(.items | any(.a == 1 or .b == 2) | not)",
			predicate.NoneMatch{Path: []string{"items"}, Cond: predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: 1.0},
				predicate.Eq{Path: []string{"b"}, Value: 2.0},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := gojq.Parse(tt.filter)
			require.NoError(t, err)

			got, ok := pushdown.Compile(q)

			require.True(t, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

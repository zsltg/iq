package redis

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidationSum(t *testing.T) {
	require.Equal(t, 5, validationSum(2, 3))
	require.Equal(t, 0, validationSum(-4, 4))
}

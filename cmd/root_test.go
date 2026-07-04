package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRootTimeoutDefault pins the default --timeout. The default bounds every
// query, so a drift in the 5*time.Second literal must not pass unnoticed.
func TestRootTimeoutDefault(t *testing.T) {
	root := newRootCmd()

	got, err := root.PersistentFlags().GetDuration("timeout")

	require.NoError(t, err)
	require.Equal(t, 5*time.Second, got)
}

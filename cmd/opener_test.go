package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

func TestSourceOpenerUnknown(t *testing.T) {
	o := newSourceOpener(&iqconfig.Config{Sources: map[string]iqconfig.Source{}}, nil)
	_, err := o.Open(context.Background(), "nope")
	require.ErrorContains(t, err, "unknown source")
	o.closeAll() // nothing opened; must not panic
}

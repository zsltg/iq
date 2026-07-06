package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/numfmt"
)

func TestSourceOpenerUnknown(t *testing.T) {
	o := newSourceOpener(&iqconfig.Config{Sources: map[string]iqconfig.Source{}}, nil, numfmt.DecimalAuto, false, false)
	_, err := o.Open(context.Background(), "nope")
	require.ErrorContains(t, err, "unknown source")
	o.closeAll() // nothing opened; must not panic
}

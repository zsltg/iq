package cmd

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestOpenMoveSourceReturnsTheHandle(t *testing.T) {
	useMoveDrivers(t)
	cfg := &config{src: "full"}

	src, closeSrc, label, err := openMoveSource(&cobra.Command{}, t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeSrc() })

	require.NotNil(t, src)
	require.Equal(t, "full", label)
}

func TestWriteModeFor(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config
		want query.WriteMode
	}{
		{"upsert by default", &config{}, query.Upsert},
		{"insert-only with --no-overwrite", &config{noOverwrite: true}, query.InsertOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, writeModeFor(tt.cfg))
		})
	}
}

func TestRenderMoveExplainDirect(t *testing.T) {
	t.Run("an omitted destination is stdout", func(t *testing.T) {
		seedMovePlan(t)
		cmd := &cobra.Command{}
		var out cappedBuffer
		cmd.SetOut(&out)

		require.NoError(t, renderMoveExplain(cmd, &config{src: "snap"}))

		require.Equal(t, "move plan\nfrom: snap\nto: stdout/stdin\n\nwrite:\n", out.String())
	})

	t.Run("a corrupt config is an error", func(t *testing.T) {
		seedMovePlan(t)
		corruptConfig(t, "")
		cmd := &cobra.Command{}
		cmd.SetOut(&cappedBuffer{})

		err := renderMoveExplain(cmd, &config{src: "snap", insert: "rd"})

		require.ErrorContains(t, err, "parse config")
	})

	t.Run("a failed write of the plan is an error", func(t *testing.T) {
		seedMovePlan(t)
		cmd := &cobra.Command{}
		w := &failingWriter{err: errors.New("disk full")}
		cmd.SetOut(w)

		err := renderMoveExplain(cmd, &config{src: "snap", insert: "rd"})

		require.ErrorIs(t, err, w.err)
	})
}

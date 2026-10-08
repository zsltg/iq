package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// TestVerifyAddKeepsTheCauseInTheChain makes sure that the verify error wraps
// its cause, so errors.Is still finds it.
func TestVerifyAddKeepsTheCauseInTheChain(t *testing.T) {
	missing := "file://" + filepath.ToSlash(filepath.Join(t.TempDir(), "missing.json"))

	err := verifyAdd(context.Background(), addedSource{name: "dump", uri: missing}, time.Second)

	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorContains(t, err, "verify dump:")
}

// TestFinishAddReturnsTheSetActiveError makes sure that a source that is not in
// the config stops --active before the save.
func TestFinishAddReturnsTheSetActiveError(t *testing.T) {
	configEnv(t)
	c := &cobra.Command{}
	c.SetErr(io.Discard)
	c.SetOut(io.Discard)

	err := finishAdd(c, newSeed(), addedSource{name: "ghost", active: true})

	require.ErrorIs(t, err, iqconfig.ErrUnknownSource)
}

// TestMvReturnsAConfigLoadError makes sure that a config that does not parse
// stops mv.
func TestMvReturnsAConfigLoadError(t *testing.T) {
	p := configEnv(t)
	require.NoError(t, os.WriteFile(p, []byte("not = [valid toml\n"), 0o600))

	_, err := runCmd(t, newMvCmd(), "a", "b")

	require.ErrorContains(t, err, "parse config")
}

// TestMvReportsAWriteFailureOfTheNothingLine makes sure that the "nothing to
// move" line returns its write error.
func TestMvReportsAWriteFailureOfTheNothingLine(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("cache", "redis://h"))
	seedConfig(t, c)
	mv := newMvCmd()
	mv.SetOut(&errAfter{0})
	mv.SetErr(io.Discard)
	mv.SetArgs([]string{"cache", "cache"})

	require.ErrorContains(t, mv.Execute(), "write failed")
}

// TestStageKeyringMovesVisitsEveryRename makes sure that a source without a
// keyring flag, or without a stored credential, does not stop the later renames.
func TestStageKeyringMovesVisitsEveryRename(t *testing.T) {
	c := newSeed()
	fk := useFakeKeyring(t)
	seedKeyringSource(t, c, fk, "inline", "redis://h:6379/0", false, "")
	seedKeyringSource(t, c, fk, "empty", "redis://h:6379/1", true, "")
	seedKeyringSource(t, c, fk, "full", "redis://h:6379/2", true, "")
	require.NoError(t, fk.Set("full-old", "pw"))
	moved := []iqconfig.Rename{
		{Old: "inline-old", New: "inline"},
		{Old: "empty-old", New: "empty"},
		{Old: "full-old", New: "full"},
	}

	staged, err := stageKeyringMoves(c, moved)

	require.NoError(t, err)
	require.Equal(t, moved[2:], staged)
	require.Equal(t, map[string]string{"full-old": "pw", "full": "pw"}, fk.m)
}

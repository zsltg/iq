package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// TestWriteKeyringRowsReportsAWriteFailureOfTheEmptyLine makes sure that the
// "no keyring-backed sources" line returns its write error.
func TestWriteKeyringRowsReportsAWriteFailureOfTheEmptyLine(t *testing.T) {
	require.ErrorContains(t, writeKeyringRows(&errAfter{0}, nil), "write failed")
}

// TestApplyMigrationReportsAWriteFailureOfTheEmptyLine makes sure that the
// "nothing to migrate" line returns its write error.
func TestApplyMigrationReportsAWriteFailureOfTheEmptyLine(t *testing.T) {
	configEnv(t)
	useFakeKeyring(t)
	require.ErrorContains(t, applyMigration(&errAfter{0}, newSeed(), nil), "write failed")
}

// TestPruneEntryReportsAWriteFailure makes sure that pruneEntry returns the error
// of its report line.
func TestPruneEntryReportsAWriteFailure(t *testing.T) {
	useFakeKeyring(t).m["stale"] = "old"

	found, err := pruneEntry(&errAfter{0}, "stale", false)

	require.ErrorContains(t, err, "write failed")
	require.False(t, found)
}

// TestPruneStaleKeepsAFindAfterALaterCleanSource makes sure that a stale entry
// found early still counts when a later source has no entry.
func TestPruneStaleKeepsAFindAfterALaterCleanSource(t *testing.T) {
	c := newSeed()
	fk := useFakeKeyring(t)
	seedKeyringSource(t, c, fk, "a-stale", "redis://h:6379/0", false, "old")
	seedKeyringSource(t, c, fk, "b-clean", "redis://h:6379/1", false, "")

	found, err := pruneStale(&errAfter{1}, c, false)

	require.NoError(t, err)
	require.True(t, found)
}

// TestInlinePasswordSourcesSkipsKeyringSources makes sure that --all does not
// target a keyring-backed source, even when its URI still holds a password.
func TestInlinePasswordSourcesSkipsKeyringSources(t *testing.T) {
	c := newSeed()
	fk := useFakeKeyring(t)
	seedKeyringSource(t, c, fk, "kr", "redis://u:pw@h:6379/0", true, "")
	seedKeyringSource(t, c, fk, "inline", "redis://u:pw@h:6379/1", false, "")

	targets, err := inlinePasswordSources(c)

	require.NoError(t, err)
	require.Equal(t, []string{"inline"}, targets)
}

// TestRefuseInlinePasswordReturnsAParseError makes sure that a URI that does not
// parse stops `config keyring set` instead of counting as password-less.
func TestRefuseInlinePasswordReturnsAParseError(t *testing.T) {
	err := refuseInlinePassword(iqconfig.Source{URL: "redis://u:p%zz@h:6379/0"}, "bad")

	require.ErrorIs(t, err, errInvalidURI)
}

// TestAdoptKeyringLeavesAKeyringSourceAlone makes sure that a source that is
// already keyring-backed returns at once, without a change to the config.
func TestAdoptKeyringLeavesAKeyringSourceAlone(t *testing.T) {
	configEnv(t)
	useFakeKeyring(t)

	err := adoptKeyring(newSeed(), iqconfig.Source{Keyring: true}, "ghost")

	require.NoError(t, err)
}

// TestAdoptKeyringRollsBackWhenTheSourceIsUnknown makes sure that a failed
// UseKeyring returns its error and deletes the new keyring entry.
func TestAdoptKeyringRollsBackWhenTheSourceIsUnknown(t *testing.T) {
	configEnv(t)
	fk := useFakeKeyring(t)
	require.NoError(t, fk.Set("ghost", "pw"))

	err := adoptKeyring(newSeed(), iqconfig.Source{URL: "redis://h"}, "ghost")

	require.ErrorIs(t, err, iqconfig.ErrUnknownSource)
	require.Equal(t, []string{"ghost"}, fk.deleted)
}

// TestStageMigrationRollsBackWhenTheURLIsBlank makes sure that a SetSourceURL
// failure returns its error and deletes every secret that the call wrote.
func TestStageMigrationRollsBackWhenTheURLIsBlank(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("sec", "redis://u:pw@h:6379/0"))
	fk := useFakeKeyring(t)
	items := []migrateItem{{keyringStaged: keyringStaged{full: "sec", clean: "sec"}, password: "pw"}}

	done, err := stageMigration(c, items)

	require.ErrorIs(t, err, iqconfig.ErrEmptyURL)
	require.Nil(t, done)
	require.Empty(t, fk.m)
}

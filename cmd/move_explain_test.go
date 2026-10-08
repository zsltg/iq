package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	iqfile "github.com/zsltg/iq/drivers/file"
)

// seedMovePlan registers the dump source "snap" and the Redis source "rd".
func seedMovePlan(t *testing.T) {
	t.Helper()
	dump := filepath.Join(t.TempDir(), "d.jsonl")
	require.NoError(t, os.WriteFile(dump, []byte(moveDump), 0o600))
	c := newSeed()
	require.NoError(t, c.Add("snap", iqfile.URL(dump)))
	require.NoError(t, c.Add("rd", "redis://h"))
	seedConfig(t, c)
}

const (
	redisUpsertOps     = "  pipeline per key: DEL then SET / HSET / RPUSH / SADD / ZADD / XADD / JSON.SET by type (replace)\n"
	redisInsertOnlyOps = "  pipeline EXISTS then, for absent keys only, SET / HSET / RPUSH / SADD / ZADD / XADD / JSON.SET by type\n"
)

func TestMoveExplain(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			"typed from stdin",
			[]string{"--typed", "--explain"},
			"move plan\nfrom: stdin\nto: typed dump (stdout/-o)\n\nwrite:\n  encode typed records\n",
		},
		{
			"typed from a source",
			[]string{"--src", "snap", "--typed", "--explain"},
			"move plan\nfrom: snap\nto: typed dump (stdout/-o)\n\nwrite:\n  encode typed records\n",
		},
		{
			"the source is named, never resolved",
			[]string{"--src", "nosuch", "--insert", "rd", "--explain"},
			"move plan\nfrom: nosuch\nto: rd\n\nwrite:\n" + redisUpsertOps,
		},
		{
			"insert upserts by default",
			[]string{"--src", "snap", "--insert", "rd", "--explain"},
			"move plan\nfrom: snap\nto: rd\n\nwrite:\n" + redisUpsertOps,
		},
		{
			"no-overwrite plans insert-only writes",
			[]string{"--src", "snap", "--insert", "rd", "--no-overwrite", "--explain"},
			"move plan\nfrom: snap\nto: rd\n\nwrite:\n" + redisInsertOnlyOps,
		},
		{
			"a dump destination lists no operations",
			[]string{"--src", "snap", "--insert", "snap", "--explain"},
			"move plan\nfrom: snap\nto: snap\n\nwrite:\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedMovePlan(t)
			root, _ := newRootCmd()
			out, err := runCmd(t, root, tt.args...)
			require.NoError(t, err)
			require.Equal(t, tt.want, out)
		})
	}

	t.Run("an unknown destination", func(t *testing.T) {
		seedMovePlan(t)
		root, _ := newRootCmd()
		_, err := runCmd(t, root, "--src", "snap", "--insert", "nope", "--explain")
		require.ErrorContains(t, err, `unknown source "nope"`)
	})

	t.Run("a corrupt config", func(t *testing.T) {
		seedMovePlan(t)
		corruptConfig(t, "")
		root, _ := newRootCmd()
		_, err := runCmd(t, root, "--src", "snap", "--insert", "rd", "--explain")
		require.ErrorContains(t, err, "parse config")
	})
}

// TestMoveCheckOrder gives each row two faults. Only the first one reports.
func TestMoveCheckOrder(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"the flag conflict comes first", []string{"--src", "snap", "--typed", "--no-overwrite", "--replace"}, "--no-overwrite and --replace are mutually exclusive"},
		{"the typed conflict comes before the filter", []string{"--src", "snap", "--typed", "--force", ".["}, "apply to --insert, not --typed"},
		{"the filter comes before the explain", []string{"--src", "snap", "--insert", "rd", "--explain", ".["}, "parse"},
		{"the filter comes before the source", []string{"--src", "nosuch", "--insert", "rd", ".["}, "parse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedMovePlan(t)
			root, _ := newRootCmd()
			_, err := runCmd(t, root, tt.args...)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	iqfile "github.com/zsltg/iq/drivers/file"
	iqconfig "github.com/zsltg/iq/internal/config"
)

func seedPingTargets() *iqconfig.Config {
	c := newSeed()
	for name, uri := range map[string]string{
		"cache": "redis://h", "prod/books": "mongodb://h/db?collection=books", "prod/cache": "redis://h",
	} {
		if err := c.Add(name, uri); err != nil {
			panic(err)
		}
	}
	return c
}

func handlesOf(targets []pingTarget) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.handle)
	}
	return out
}

func TestPingTargetsNamedEdges(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"a repeated source counts once", []string{"cache", "cache"}, []string{"cache"}},
		{"a group and its member count once", []string{"prod", "prod/books"}, []string{"prod/books", "prod/cache"}},
		{"a member and its group keep the member first", []string{"prod/cache", "prod"}, []string{"prod/cache", "prod/books"}},
		{"arguments keep their order", []string{"prod", "cache"}, []string{"prod/books", "prod/cache", "cache"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targets, err := pingTargets(seedPingTargets(), tt.args, false)
			require.NoError(t, err)
			require.Equal(t, tt.want, handlesOf(targets))
		})
	}

	t.Run("unknown names are cleaned, ordered and reported together", func(t *testing.T) {
		_, err := pingTargets(seedPingTargets(), []string{"@nope", "cache", "gone"}, false)
		require.EqualError(t, err, "unknown source or group: nope, gone")
	})

	t.Run("a prefix without the slash is not a group", func(t *testing.T) {
		_, err := pingTargets(seedPingTargets(), []string{"pro"}, false)
		require.EqualError(t, err, "unknown source or group: pro")
	})
}

func TestPingTargetsActiveNotFound(t *testing.T) {
	c := seedPingTargets()
	c.Active = "ghost"
	_, err := pingTargets(c, nil, false)
	require.EqualError(t, err, "active source \"ghost\" not found; run `iq ls`")
}

func TestPingTargetsAllChecksArgsFirst(t *testing.T) {
	_, err := pingTargets(newSeed(), []string{"x"}, true)
	require.ErrorContains(t, err, "--all pings every source")
}

func TestPingTable(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "d.jsonl")
	require.NoError(t, os.WriteFile(dump, []byte(moveDump), 0o600))
	c := newSeed()
	require.NoError(t, c.Add("a-dump", iqfile.URL(dump)))
	require.NoError(t, c.Add("b-dead", "redis://127.0.0.1:1"))
	seedConfig(t, c)

	t.Run("an ok row and an error row, in target order", func(t *testing.T) {
		out, err := runCmd(t, newPingCmd(&config{timeout: 2 * time.Second}), "a-dump", "b-dead")
		require.EqualError(t, err, "one or more sources unreachable")
		require.Regexp(t, regexp.MustCompile(`(?m)^a-dump\s+file\s+ok\s+(0s|\d+(\.\d+)?m?s)\s*$`), out)
		require.Regexp(t, regexp.MustCompile(`(?m)^b-dead\s+redis\s+error\s+\S.*$`), out)
		require.Less(t, regexp.MustCompile(`a-dump`).FindStringIndex(out)[0], regexp.MustCompile(`b-dead`).FindStringIndex(out)[0])
	})

	t.Run("all reachable gives no error", func(t *testing.T) {
		out, err := runCmd(t, newPingCmd(&config{timeout: 2 * time.Second}), "a-dump")
		require.NoError(t, err)
		require.Contains(t, out, "ok")
		require.NotContains(t, out, "error")
	})
}

func TestPingChecksAreBoundedByTheTimeout(t *testing.T) {
	rec := useCtxDriver(t)
	cmd := newPingCmd(&config{timeout: 7 * time.Second})
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"a"})
	require.NoError(t, cmd.ExecuteContext(context.WithValue(context.Background(), diffCtxKey{}, "marker")))

	require.Equal(t, []string{`{"ping":1}`}, rec.queries["ctxrec://a"])
	require.NotEmpty(t, rec.ctxs["ctxrec://a"])
	for _, ctx := range rec.ctxs["ctxrec://a"] {
		require.Equal(t, "marker", ctx.Value(diffCtxKey{}))
		dl, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(dl), 7*time.Second)
	}
}

func TestVerifySourceSkipsTheQuery(t *testing.T) {
	tests := []struct {
		name  string
		flags driver
		query int
	}{
		{"a read-only driver", driver{readOnly: true}, 0},
		{"a driver that verifies at open", driver{verifiesOnOpen: true}, 0},
		{"any other driver", driver{}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &ctxRecorder{ctxs: map[string][]context.Context{}, queries: map[string][]string{}, scanned: map[string]int{}}
			d := tt.flags
			d.name, d.schemes = "vrec", []string{"vrec"}
			d.open = func(ctx context.Context, cfg *config) (store, error) {
				return ctxRecordingStore{rec: rec, url: cfg.url}, nil
			}
			orig := drivers
			t.Cleanup(func() { drivers = orig })
			drivers = append(append([]driver{}, orig...), d)

			require.NoError(t, verifySource(context.Background(), "vrec://h", time.Second))
			require.Len(t, rec.queries["vrec://h"], tt.query)
		})
	}
}

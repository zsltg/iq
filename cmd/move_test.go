package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
)

// seedFileSource writes a typed JSONL dump and registers it as the source "snap".
func seedFileSource(t *testing.T, jsonl string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dump.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(jsonl), 0o600))
	c := newSeed()
	require.NoError(t, c.Add("snap", "file://"+path))
	seedConfig(t, c)
}

const moveDump = `{"key":"a","type":"string","value":"hi"}
{"key":"b","type":"hash","value":{"f":"v"}}
`

func TestTypedDumpFormatsFromFileSource(t *testing.T) {
	seedFileSource(t, moveDump)
	cases := []struct{ name, flag, want string }{
		{"jsonl default", "", `{"key":"a","type":"string","value":"hi"}`},
		{"jsonl", "--jsonl", `{"key":"b","type":"hash","value":{"f":"v"}}`},
		{"jsona", "--jsona", `"key": "a"`},
		{"yaml", "--yaml", "key: a"},
		{"json", "--json", "\"key\": \"a\",\n  \"type\": \"string\","},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, _ := newRootCmd()
			args := []string{"--src", "snap", "--typed"}
			if c.flag != "" {
				args = append(args, c.flag)
			}
			out, err := runCmd(t, root, args...)
			require.NoError(t, err)
			require.Contains(t, out, c.want)
		})
	}
}

// TestTypedRejectsUnimportableFormats covers the renderings that cannot carry a
// {key,type,value} record back through a file:// source. Both spellings of gron
// are exercised: the shorthand once fell through to the jsonl default instead of
// reaching the rejection.
func TestTypedRejectsUnimportableFormats(t *testing.T) {
	seedFileSource(t, moveDump)
	cases := []struct{ name, flag, want string }{
		{"raw", "--raw", "--typed cannot use --raw/--values"},
		{"gron", "--gron", "--typed cannot use --gron/--grona"},
		{"grona", "--grona", "--typed cannot use --gron/--grona"},
		{"format gron", "--format=gron", "--typed cannot use --gron/--grona"},
		{"format grona", "--format=grona", "--typed cannot use --gron/--grona"},
		{"format parquet", "--format=parquet", "--typed cannot use --format parquet"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, _ := newRootCmd()
			out, err := runCmd(t, root, "--src", "snap", "--typed", c.flag)
			require.ErrorContains(t, err, c.want)
			require.ErrorContains(t, err, "choose --jsonl (default), --json, --jsona, or --yaml")
			require.Empty(t, out, "a rejected format must not emit records")
		})
	}
}

// TestTypedJSONDumpReimports guards the claim that every accepted --typed
// rendering round-trips: the pretty-json dump is re-read as a file:// source and
// yields the records it was built from.
func TestTypedJSONDumpReimports(t *testing.T) {
	seedFileSource(t, moveDump)
	root, _ := newRootCmd()
	dump, err := runCmd(t, root, "--src", "snap", "--typed", "--json")
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "dump.json")
	require.NoError(t, os.WriteFile(path, []byte(dump), 0o600))
	c := newSeed()
	require.NoError(t, c.Add("back", "file://"+path))
	seedConfig(t, c)

	root2, _ := newRootCmd()
	out, err := runCmd(t, root2, "--src", "back", "--typed")
	require.NoError(t, err)
	require.Contains(t, out, `{"key":"a","type":"string","value":"hi"}`)
	require.Contains(t, out, `{"key":"b","type":"hash","value":{"f":"v"}}`)
}

// TestQueryStdin exercises the sq-style implicit stdin: with no source selected, a
// piped dump is the source. stdinIsTerminal is forced to report a pipe.
func TestQueryStdin(t *testing.T) {
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = orig })
	t.Setenv(iqconfig.EnvConfig, filepath.Join(t.TempDir(), "iq.toml"))

	root, _ := newRootCmd()
	root.SetIn(strings.NewReader(moveDump))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{".b"})
	require.NoError(t, root.Execute())
	require.Contains(t, out.String(), `"f": "v"`)
}

// TestMoveInsertRoundTripRedisIntegration restores a typed dump into a live Redis
// via --insert, then dumps it back with --typed and checks the records survive.
func TestMoveInsertRoundTripRedisIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable Redis")
	}
	base := redisBaseURL()
	srcURL, err := withRedisDB(base, testRedisDB)
	require.NoError(t, err)
	dstURL, err := withRedisDB(base, testRedisDBAlt)
	require.NoError(t, err)
	seedRedis(t, srcURL, map[string]string{"k1": "v1", "k2": "v2"})

	c := newSeed()
	require.NoError(t, c.Add("src", srcURL))
	require.NoError(t, c.Add("dst", dstURL))
	seedConfig(t, c)

	root, _ := newRootCmd()
	_, err = runCmd(t, root, "--timeout", "5s", "--src", "src", "--insert", "dst", "--replace", "--force")
	require.NoError(t, err)

	root2, _ := newRootCmd()
	out, err := runCmd(t, root2, "--timeout", "5s", "--src", "dst", "--typed")
	require.NoError(t, err)
	require.Contains(t, out, `"key":"k1"`)
	require.Contains(t, out, `"key":"k2"`)
	require.Contains(t, out, `"value":"v1"`)
}

// TestInsertRequestFor pins how the CLI flags become the write request both
// delivery mechanisms share: --no-overwrite picks insert-only, --replace and
// --dry-run pass through, and --force makes the confirmation a no-op.
func TestInsertRequestFor(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config
		wantMode    query.WriteMode
		wantReplace bool
		wantDryRun  bool
		wantConfirm bool
	}{
		{name: "defaults upsert", cfg: config{insert: "cache"}, wantMode: query.Upsert},
		{name: "no-overwrite is insert-only", cfg: config{insert: "cache", noOverwrite: true}, wantMode: query.InsertOnly},
		{name: "replace and dry-run pass through", cfg: config{insert: "cache", replace: true, dryRun: true}, wantMode: query.Upsert, wantReplace: true, wantDryRun: true},
		{name: "force confirms without asking", cfg: config{insert: "cache", force: true}, wantMode: query.Upsert, wantConfirm: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.SetIn(strings.NewReader(""))
			cmd.SetErr(&bytes.Buffer{})
			req := insertRequestFor(cmd, &tc.cfg)
			require.Equal(t, "cache", req.dst)
			require.Equal(t, tc.wantMode, req.mode)
			require.Equal(t, tc.wantReplace, req.replace)
			require.Equal(t, tc.wantDryRun, req.dryRun)
			require.NotNil(t, req.confirm)
			err := req.confirm("clear cache")
			if tc.wantConfirm {
				require.NoError(t, err)
				return
			}
			// Without --force the confirmation refuses: with no terminal it asks
			// for --force, on a terminal the empty answer aborts.
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), "needs confirmation") || err.Error() == "aborted", err.Error())
		})
	}
}

// TestApplyInsertRedisIntegration drives applyInsert against a reachable Redis:
// a dry-run replace neither asks nor clears, insert-only skips an existing key,
// and the transform shapes what is written.
func TestApplyInsertRedisIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable Redis")
	}
	u := os.Getenv("IQ_REDIS_URL")
	if u == "" {
		u = "redis://localhost:6379/0"
	}
	c := newSeed()
	require.NoError(t, c.Add("cache", u))
	seedConfig(t, c)
	ctx := context.Background()
	get := func(t *testing.T, key string) any {
		t.Helper()
		st, err := openStore(ctx, &config{url: u})
		require.NoError(t, err)
		defer func() { _ = st.Close() }()
		v, err := query.NewRunner(st).Run(ctx, []string{"GET", key})
		require.NoError(t, err)
		return v
	}
	records := func(recs ...query.Record) query.RecordSource {
		return func(_ context.Context, fn func([]query.Record) error) error { return fn(recs) }
	}
	identity := func(r query.Record) ([]query.Record, error) { return []query.Record{r}, nil }

	t.Run("a dry-run replace asks nothing and clears nothing", func(t *testing.T) {
		seedRedis(t, u, map[string]string{"keep": "1"})
		req := insertRequest{dst: "cache", mode: query.Upsert, replace: true, dryRun: true, confirm: func(action string) error {
			t.Fatalf("confirmation asked on a dry run: %s", action)
			return nil
		}}
		_, stat, err := applyInsert(ctx, req, records(query.Record{Key: "new", Value: "2"}), identity)
		require.NoError(t, err)
		require.Equal(t, 1, stat.Written)
		require.Equal(t, "1", fmt.Sprint(get(t, "keep")))
		require.Nil(t, get(t, "new"))
	})

	t.Run("a replace asks, then clears", func(t *testing.T) {
		seedRedis(t, u, map[string]string{"old": "1"})
		var asked string
		req := insertRequest{dst: "cache", mode: query.Upsert, replace: true, confirm: func(action string) error {
			asked = action
			return nil
		}}
		label, stat, err := applyInsert(ctx, req, records(query.Record{Key: "new", Value: "2"}), identity)
		require.NoError(t, err)
		require.Equal(t, "cache", label)
		require.Contains(t, asked, "clear")
		require.Equal(t, 1, stat.Written)
		require.Nil(t, get(t, "old"))
		require.Equal(t, "2", fmt.Sprint(get(t, "new")))
	})

	t.Run("a refused confirmation aborts before clearing", func(t *testing.T) {
		seedRedis(t, u, map[string]string{"old": "1"})
		req := insertRequest{dst: "cache", mode: query.Upsert, replace: true, confirm: func(string) error {
			return errors.New("aborted")
		}}
		_, _, err := applyInsert(ctx, req, records(query.Record{Key: "new", Value: "2"}), identity)
		require.ErrorContains(t, err, "aborted")
		require.Equal(t, "1", fmt.Sprint(get(t, "old")))
		require.Nil(t, get(t, "new"))
	})

	t.Run("insert-only skips an existing key", func(t *testing.T) {
		seedRedis(t, u, map[string]string{"k": "old"})
		req := insertRequest{dst: "cache", mode: query.InsertOnly}
		_, stat, err := applyInsert(ctx, req, records(query.Record{Key: "k", Value: "new"}), identity)
		require.NoError(t, err)
		require.Equal(t, 1, stat.Skipped)
		require.Equal(t, "old", fmt.Sprint(get(t, "k")))

		req.mode = query.Upsert
		_, stat, err = applyInsert(ctx, req, records(query.Record{Key: "k", Value: "new"}), identity)
		require.NoError(t, err)
		require.Equal(t, 1, stat.Overwritten)
		require.Equal(t, "new", fmt.Sprint(get(t, "k")))
	})

	t.Run("runInsert reports the outcome", func(t *testing.T) {
		seedRedis(t, u, nil)
		cmd := &cobra.Command{}
		var stderr bytes.Buffer
		cmd.SetErr(&stderr)
		cfg := &config{insert: "cache"}
		require.NoError(t, runInsert(cmd, ctx, cfg, records(query.Record{Key: "r", Value: "1"}), identity))
		require.Contains(t, stderr.String(), "wrote 1 item(s) to cache")
	})

	t.Run("the transform shapes what is written", func(t *testing.T) {
		seedRedis(t, u, nil)
		req := insertRequest{dst: "cache", mode: query.Upsert}
		upper := func(r query.Record) ([]query.Record, error) {
			return []query.Record{{Key: "x:" + r.Key, Value: strings.ToUpper(fmt.Sprint(r.Value))}}, nil
		}
		_, stat, err := applyInsert(ctx, req, records(query.Record{Key: "a", Value: "v"}), upper)
		require.NoError(t, err)
		require.Equal(t, 1, stat.Written)
		require.Nil(t, get(t, "a"))
		require.Equal(t, "V", fmt.Sprint(get(t, "x:a")))
	})
}

// TestApplyInsertNeedsADestination proves an empty destination is refused as a
// missing saved source: the omitted-destination-is-stdout rule of a dump does not
// apply to a write.
func TestApplyInsertNeedsADestination(t *testing.T) {
	seedConfig(t, newSeed())
	src := func(_ context.Context, fn func([]query.Record) error) error { return fn(nil) }
	_, _, err := applyInsert(context.Background(), insertRequest{dst: ""}, src, nil)
	require.ErrorContains(t, err, "--insert must name a saved source")
}

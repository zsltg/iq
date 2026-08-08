package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
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

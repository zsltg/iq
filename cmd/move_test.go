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
	require.NoError(t, c.Add("snap", "file://"+path, ""))
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
		{"json-array", "--json-array", `"key": "a"`},
		{"yaml", "--yaml", "key: a"},
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

func TestTypedRejectsValuesFormat(t *testing.T) {
	seedFileSource(t, moveDump)
	root, _ := newRootCmd()
	_, err := runCmd(t, root, "--src", "snap", "--typed", "--raw")
	require.ErrorContains(t, err, "cannot use --raw")
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
	require.NoError(t, c.Add("src", srcURL, ""))
	require.NoError(t, c.Add("dst", dstURL, ""))
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

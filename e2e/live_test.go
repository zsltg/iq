package e2e

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	iqfile "github.com/zsltg/iq/drivers/file"
)

// e2eRedisDB is the logical Redis database the live e2e round-trip owns. The reserved
// databases are partitioned so no suite flushes another's data: 15 and 14 are the cmd
// integration scratch pair (cmd/diff_test.go), 13 is the redis driver's private DB
// (drivers/redis), and 12 belongs to these black-box e2e flows.
const e2eRedisDB = 12

// requireLive returns the backend URL from envVar, skipping the test under -short
// (where the binary is never built) or when envVar is unset. There is deliberately no
// localhost fallback: a live flow runs only when its URL is explicitly provided, so the
// offline suite never depends on a reachable server.
func requireLive(t *testing.T, envVar string) string {
	t.Helper()
	skipShort(t)
	u := os.Getenv(envVar)
	if u == "" {
		t.Skipf("live e2e: set %s to a running backend to run this flow", envVar)
	}
	return u
}

// withRedisDB rewrites the database number in a redis URL's path, so a flow runs
// against its reserved DB rather than whatever the base URL points at.
func withRedisDB(raw string, db int) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse redis url: %w", err)
	}
	u.Path = "/" + strconv.Itoa(db)
	return u.String(), nil
}

// withMongoTarget rewrites a mongo URL to a specific database path and ?collection=,
// so a flow runs against its own throwaway collection.
func withMongoTarget(raw, db, coll string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse mongo url: %w", err)
	}
	u.Path = "/" + db
	q := u.Query()
	q.Set("collection", coll)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func TestLiveRedisRoundTrip(t *testing.T) {
	base := requireLive(t, "IQ_REDIS_URL")
	redisURL, err := withRedisDB(base, e2eRedisDB)
	require.NoError(t, err)

	dir := t.TempDir()
	env := []string{"IQ_CONFIG=" + filepath.Join(dir, "iq.toml")}

	// A typed dump carries a Redis string and a hash — their types live outside the
	// value, so a typed record is what round-trips them.
	dump := filepath.Join(dir, "seed.jsonl")
	require.NoError(t, os.WriteFile(dump, []byte(
		`{"key":"iq_e2e:greeting","type":"string","value":"hello-iq"}`+"\n"+
			`{"key":"iq_e2e:book","type":"hash","value":{"title":"Dune"}}`+"\n",
	), 0o600))

	// add echoes the URL, so its failure message must not; the rest carry only handles.
	if _, stderr, code := run(t, env, "add", redisURL, "-n", "rlive"); code != 0 {
		t.Fatalf("add rlive failed: %s", stderr)
	}
	if _, stderr, code := run(t, env, "add", iqfile.URL(dump), "-n", "rseed"); code != 0 {
		t.Fatalf("add rseed failed: %s", stderr)
	}
	// Start from an empty reserved DB so the counts below are deterministic.
	mustRun(t, env, "data", "clear", "rlive", "--force")

	_, stderr, code := run(t, env, "--src", "rseed", "--insert", "rlive")
	require.Zerof(t, code, "insert failed: %s", stderr)
	require.Contains(t, stderr, "new 2") // the write summary is progress, on stderr

	// A bounded .[] scan streams the values back out of the keyspace.
	out, stderr, code := run(t, env, "--src", "rlive", ".[]", "--jsonl")
	require.Zerof(t, code, "scan failed: %s", stderr)
	require.Contains(t, out, "hello-iq")
	require.Contains(t, out, "Dune")

	// exec forwards a raw command: the hash key kept its native Redis type.
	out, stderr, code = run(t, env, "--src", "rlive", "exec", "TYPE", "iq_e2e:book")
	require.Zerof(t, code, "exec TYPE failed: %s", stderr)
	require.Contains(t, out, "hash")

	// data delete removes named keys; the report is honest about a key already absent,
	// and no prompt is needed since the key list is the confirmation.
	_, stderr, code = run(t, env, "data", "delete", "rlive", "iq_e2e:greeting", "iq_e2e:absent")
	require.Zerof(t, code, "delete failed: %s", stderr)
	require.Contains(t, stderr, "deleted 1 key(s), 1 already absent")
	// The deleted key now reads null; the untouched key still resolves.
	out, stderr, code = run(t, env, "--src", "rlive", `.["iq_e2e:greeting"]`)
	require.Zerof(t, code, "get after delete failed: %s", stderr)
	require.Contains(t, out, "null")
	out, stderr, code = run(t, env, "--src", "rlive", "exec", "TYPE", "iq_e2e:book")
	require.Zerof(t, code, "exec TYPE after delete failed: %s", stderr)
	require.Contains(t, out, "hash")

	// Clear empties the DB; DBSIZE proves nothing is left.
	mustRun(t, env, "data", "clear", "rlive", "--force")
	out, stderr, code = run(t, env, "--src", "rlive", "exec", "DBSIZE")
	require.Zerof(t, code, "exec DBSIZE failed: %s", stderr)
	require.Contains(t, out, "(integer) 0")
}

func TestLiveMongoRoundTrip(t *testing.T) {
	base := requireLive(t, "IQ_MONGO_URL")
	// A per-process collection keeps concurrent runs from colliding; it is dropped at
	// the end regardless of how the test exits.
	coll := fmt.Sprintf("iq_e2e_%d", os.Getpid())
	mongoURL, err := withMongoTarget(base, "iq_e2e", coll)
	require.NoError(t, err)

	dir := t.TempDir()
	env := []string{"IQ_CONFIG=" + filepath.Join(dir, "iq.toml")}

	if _, stderr, code := run(t, env, "add", mongoURL, "-n", "mlive"); code != 0 {
		t.Fatalf("add mlive failed: %s", stderr)
	}
	// Cleanup runs after t.Context() is canceled, so drop under a background context.
	t.Cleanup(func() { _, _, _ = runCtx(t, context.Background(), "", env, "data", "drop", "mlive", "--force") })

	// Typed JSONL on stdin is an implicit source; each value is a document.
	seed := `{"key":"1","type":"document","value":{"title":"Dune","year":1965}}` + "\n" +
		`{"key":"2","type":"document","value":{"title":"Hyperion","year":1989}}` + "\n"
	_, stderr, code := runIn(t, seed, env, "--insert", "mlive")
	require.Zerof(t, code, "insert failed: %s", stderr)
	require.Contains(t, stderr, "new 2") // the write summary is progress, on stderr

	// A bounded .[] scan streams both documents back.
	out, stderr, code := run(t, env, "--src", "mlive", ".[]", "--jsonl")
	require.Zerof(t, code, "scan failed: %s", stderr)
	require.Contains(t, out, "Dune")
	require.Contains(t, out, "Hyperion")

	// A bounded key read fetches one document by its _id.
	out, stderr, code = run(t, env, "--src", "mlive", `.["1"]`)
	require.Zerof(t, code, "get failed: %s", stderr)
	require.Contains(t, out, "Dune")

	countCmd := fmt.Sprintf(`{"count":%q}`, coll)
	out, stderr, code = run(t, env, "--src", "mlive", "exec", countCmd)
	require.Zerof(t, code, "exec count failed: %s", stderr)
	require.Contains(t, out, `"n": 2`)

	// data delete removes one document by _id; a key already absent is reported, not an
	// error, and the surviving document still reads back.
	_, stderr, code = run(t, env, "data", "delete", "mlive", "1", "absent")
	require.Zerof(t, code, "delete failed: %s", stderr)
	require.Contains(t, stderr, "deleted 1 key(s), 1 already absent")
	out, stderr, code = run(t, env, "--src", "mlive", `.["1"]`)
	require.Zerof(t, code, "get after delete failed: %s", stderr)
	require.Contains(t, out, "null")
	out, stderr, code = run(t, env, "--src", "mlive", `.["2"]`)
	require.Zerof(t, code, "get survivor after delete failed: %s", stderr)
	require.Contains(t, out, "Hyperion")

	// Clear empties the collection; the count drops to zero.
	mustRun(t, env, "data", "clear", "mlive", "--force")
	out, stderr, code = run(t, env, "--src", "mlive", "exec", countCmd)
	require.Zerof(t, code, "exec count after clear failed: %s", stderr)
	require.Contains(t, out, `"n": 0`)
}

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/diff"
	"github.com/zsltg/iq/internal/query"
)

// quietDiff builds a diff command that suppresses cobra's error/usage printing,
// as the real Execute() does for errQuietExit, so a differing-source run leaves
// only the rendered delta in the captured buffer for decoding.
func quietDiff(cfg *config) *cobra.Command {
	dc := newDiffCmd(cfg)
	dc.SilenceErrors, dc.SilenceUsage = true, true
	return dc
}

func TestReportEmpty(t *testing.T) {
	require.True(t, report{}.empty())
	require.False(t, report{Data: []diff.ItemDelta{{Key: "x", Op: diff.OpAdd}}}.empty())
	require.False(t, report{Stats: []diff.Change{{Op: diff.OpChange}}}.empty())
	require.False(t, report{Schema: []diff.Change{{Op: diff.OpAdd}}}.empty())
}

func TestReportRenderHuman(t *testing.T) {
	rep := report{
		Data: []diff.ItemDelta{
			{Key: "gone", Op: diff.OpRemove, Old: map[string]any{"v": 1}},
			{Key: "keep", Op: diff.OpChange, Changes: []diff.Change{{Path: []string{"v"}, Op: diff.OpChange, Old: 1, New: 9}}},
			{Key: "new", Op: diff.OpAdd, New: map[string]any{"v": 3}},
		},
		Schema:    []diff.Change{{Path: []string{".email"}, Op: diff.OpAdd, New: map[string]any{"types": []any{"string"}}}},
		dataRun:   true,
		schemaRun: true,
	}
	left := diffTarget{handle: "a", driver: "redis"}
	right := diffTarget{handle: "b", driver: "redis"}
	var buf bytes.Buffer
	require.NoError(t, rep.render(&buf, left, right, false, false))
	out := buf.String()

	require.Contains(t, out, "a (redis)  →  b (redis)")
	require.Contains(t, out, "# data")
	require.Contains(t, out, "- gone")
	require.Contains(t, out, "~ keep")
	require.Contains(t, out, "    ~ v: 1 → 9")
	require.Contains(t, out, "+ new")
	require.Contains(t, out, "1 added, 1 removed, 1 changed")
	require.Contains(t, out, "# schema")
	require.Contains(t, out, "+ .email")
}

func TestReportRenderNoDifferences(t *testing.T) {
	rep := report{Data: []diff.ItemDelta{}, dataRun: true}
	var buf bytes.Buffer
	require.NoError(t, rep.render(&buf, diffTarget{handle: "a"}, diffTarget{handle: "b"}, false, false))
	require.Contains(t, buf.String(), "no differences")
}

func TestReportRenderJSON(t *testing.T) {
	rep := report{
		Data:    []diff.ItemDelta{{Key: "new", Op: diff.OpAdd, New: map[string]any{"v": 3}}},
		dataRun: true,
	}
	var buf bytes.Buffer
	require.NoError(t, rep.render(&buf, diffTarget{handle: "a"}, diffTarget{handle: "b"}, true, false))

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	data := got["data"].([]any)
	require.Len(t, data, 1)
	first := data[0].(map[string]any)
	require.Equal(t, "new", first["key"])
	require.Equal(t, "add", first["op"]) // Op marshals as its name, not an integer
}

func TestResolveDiffTargetUnknown(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("known", "redis://h:6379/0"))
	seedConfig(t, c)
	cf, err := iqconfig.Load()
	require.NoError(t, err)

	_, err = resolveDiffTarget(cf, "missing")
	require.ErrorContains(t, err, "unknown source")

	tgt, err := resolveDiffTarget(cf, "known")
	require.NoError(t, err)
	require.Equal(t, "redis", tgt.driver)
	require.Equal(t, "redis://h:6379/0", tgt.url)
}

func TestDiffStatsRejectsCrossDriver(t *testing.T) {
	// The scheme check happens before any store is opened, so no connection is made.
	_, err := diffStats(context.Background(),
		diffTarget{handle: "a", driver: "redis"},
		diffTarget{handle: "b", driver: "mongo"}, nil, diff.Options{})
	require.ErrorContains(t, err, "same driver")
}

func TestRedisInfoTree(t *testing.T) {
	got := redisInfoTree("# Server\r\nredis_version:7.2.0\r\n\r\n# Memory\r\nused_memory:12345\r\n")
	server := got["Server"].(map[string]any)
	require.Equal(t, "7.2.0", server["redis_version"])
	mem := got["Memory"].(map[string]any)
	require.Equal(t, "12345", mem["used_memory"])
}

func TestSideValue(t *testing.T) {
	require.Equal(t, "old", sideValue(diff.OpRemove, "old", "new"))
	require.Equal(t, "new", sideValue(diff.OpAdd, "old", "new"))
}

func TestSampleFull(t *testing.T) {
	tests := []struct {
		count, sample int
		want          bool
	}{
		{0, 0, false},   // sample 0 means unbounded: never full
		{100, 0, false}, // still unbounded
		{1, 3, false},   // below the cap
		{2, 2, true},    // exactly at the cap
		{5, 3, true},    // past the cap
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("count=%d,sample=%d", tt.count, tt.sample), func(t *testing.T) {
			require.Equal(t, tt.want, sampleFull(tt.count, tt.sample))
		})
	}
}

func TestCompactFallsBackOnUnmarshalable(t *testing.T) {
	// A channel cannot be JSON-marshaled, so compact falls back to %v formatting.
	ch := make(chan int)
	require.Equal(t, fmt.Sprintf("%v", ch), compact(ch))
	// The happy path still emits JSON.
	require.Equal(t, `{"a":1}`, compact(map[string]any{"a": 1}))
}

// errAfter is an io.Writer that succeeds n times, then fails every write. It
// drives the error-propagation paths of the render helpers.
type errAfter struct{ n int }

func (w *errAfter) Write(p []byte) (int, error) {
	if w.n <= 0 {
		return 0, fmt.Errorf("write failed")
	}
	w.n--
	return len(p), nil
}

// failAt is an io.Writer that fails on exactly the at-th write (1-indexed) and
// succeeds on every other. It isolates a single error return: if a helper wrongly
// swallowed that error and continued, the later writes still succeed, so the
// missing propagation surfaces as a nil result.
type failAt struct{ at, n int }

func (w *failAt) Write(p []byte) (int, error) {
	w.n++
	if w.n == w.at {
		return 0, fmt.Errorf("write failed")
	}
	return len(p), nil
}

func TestRenderPropagatesWriteErrors(t *testing.T) {
	change := diff.Change{Path: []string{"v"}, Op: diff.OpChange, Old: 1, New: 2}
	add := diff.Change{Path: []string{"v"}, Op: diff.OpAdd, New: 2}
	item := diff.ItemDelta{Key: "k", Op: diff.OpChange, Changes: []diff.Change{change}}
	addItem := diff.ItemDelta{Key: "k", Op: diff.OpAdd, New: 1}

	t.Run("renderChangeLine change", func(t *testing.T) {
		require.Error(t, renderChangeLine(&errAfter{0}, "", change))
	})
	t.Run("renderChangeLine add", func(t *testing.T) {
		require.Error(t, renderChangeLine(&errAfter{0}, "", add))
	})
	t.Run("renderChanges heading", func(t *testing.T) {
		require.Error(t, renderChanges(&errAfter{0}, "t", []diff.Change{change}))
	})
	t.Run("renderChanges body", func(t *testing.T) {
		require.Error(t, renderChanges(&errAfter{1}, "t", []diff.Change{change}))
	})
	t.Run("renderChanges body error is returned not swallowed", func(t *testing.T) {
		// Fail only the change-line write (2nd): heading and summary succeed, so a
		// helper that dropped the renderChangeLine error would return nil.
		require.Error(t, renderChanges(&failAt{at: 2}, "t", []diff.Change{change}))
	})
	t.Run("renderItems change-line error is returned not swallowed", func(t *testing.T) {
		// Writes: heading(1), item key line(2), change line(3), summary(4). Fail the
		// change line only.
		require.Error(t, renderItems(&failAt{at: 3}, "t", []diff.ItemDelta{item}))
	})
	t.Run("renderItems heading", func(t *testing.T) {
		require.Error(t, renderItems(&errAfter{0}, "t", []diff.ItemDelta{item}))
	})
	t.Run("renderItems change body", func(t *testing.T) {
		require.Error(t, renderItems(&errAfter{1}, "t", []diff.ItemDelta{item}))
	})
	t.Run("renderItems add body", func(t *testing.T) {
		require.Error(t, renderItems(&errAfter{1}, "t", []diff.ItemDelta{addItem}))
	})
	t.Run("writeSummary empty", func(t *testing.T) {
		require.Error(t, writeSummary(&errAfter{0}, diff.Summary{}))
	})
	t.Run("writeSummary counts", func(t *testing.T) {
		require.Error(t, writeSummary(&errAfter{0}, diff.Summary{Added: 1}))
	})
	t.Run("render header", func(t *testing.T) {
		rep := report{dataRun: true}
		require.Error(t, rep.render(&errAfter{0}, diffTarget{}, diffTarget{}, false, false))
	})
	t.Run("render data section", func(t *testing.T) {
		rep := report{Data: []diff.ItemDelta{item}, dataRun: true}
		require.Error(t, rep.render(&errAfter{1}, diffTarget{}, diffTarget{}, false, false))
	})
	t.Run("render stats section", func(t *testing.T) {
		rep := report{Stats: []diff.Change{change}, statsRun: true}
		require.Error(t, rep.render(&errAfter{1}, diffTarget{}, diffTarget{}, false, false))
	})
	t.Run("render schema section", func(t *testing.T) {
		rep := report{Schema: []diff.Change{change}, schemaRun: true}
		require.Error(t, rep.render(&errAfter{1}, diffTarget{}, diffTarget{}, false, false))
	})
}

func TestDiffDataRedisIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable Redis")
	}
	base := redisBaseURL()
	urlA, err := withRedisDB(base, testRedisDB)
	require.NoError(t, err)
	urlB, err := withRedisDB(base, testRedisDBAlt)
	require.NoError(t, err)
	seedRedis(t, urlA, map[string]string{"k1": "v1", "shared": "old"})
	seedRedis(t, urlB, map[string]string{"k2": "v2", "shared": "new"})

	c := newSeed()
	require.NoError(t, c.Add("a", urlA))
	require.NoError(t, c.Add("b", urlB))
	seedConfig(t, c)

	cfg := &config{timeout: 5 * time.Second}
	out, err := runCmd(t, quietDiff(cfg), "a", "b", "--json")
	// The sources differ, so diff writes the delta and returns the quiet-exit
	// sentinel (diff(1)-style non-zero exit).
	require.ErrorIs(t, err, errQuietExit)

	deltas := decodeData(t, out)
	require.Equal(t, map[string]string{"k1": "remove", "k2": "add", "shared": "change"}, deltas)
}

func TestDiffExitCodeRedisIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable Redis")
	}
	base := redisBaseURL()
	urlA, err := withRedisDB(base, testRedisDB)
	require.NoError(t, err)
	urlB, err := withRedisDB(base, testRedisDBAlt)
	require.NoError(t, err)
	seedRedis(t, urlA, map[string]string{"only": "here"})
	seedRedis(t, urlB, map[string]string{"different": "value"})

	c := newSeed()
	require.NoError(t, c.Add("a", urlA))
	require.NoError(t, c.Add("b", urlB))
	seedConfig(t, c)

	cfg := &config{timeout: 5 * time.Second}

	// a vs a: identical, so diff exits zero.
	_, err = runCmd(t, newDiffCmd(cfg), "a", "a")
	require.NoError(t, err)

	// a vs b: differ, so diff returns the quiet-exit sentinel (non-zero exit),
	// diff(1)-style, with no flag needed.
	_, err = runCmd(t, newDiffCmd(cfg), "a", "b")
	require.ErrorIs(t, err, errQuietExit)
}

func TestDiffDataAndSchemaMongoIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable MongoDB")
	}
	base := mongoBaseURL()
	seedMongo(t, base, "ca", []string{`{"_id":"1","name":"a"}`, `{"_id":"2","name":"x"}`})
	seedMongo(t, base, "cb", []string{`{"_id":"2","name":"y"}`, `{"_id":"3","name":"c","email":"e@x"}`})

	c := newSeed()
	require.NoError(t, c.Add("a", base+"?collection=ca"))
	require.NoError(t, c.Add("b", base+"?collection=cb"))
	seedConfig(t, c)

	cfg := &config{timeout: 8 * time.Second}

	data, err := runCmd(t, quietDiff(cfg), "a", "b", "--json")
	// Differing sources exit non-zero (diff(1)-style) while still writing the delta.
	require.ErrorIs(t, err, errQuietExit)
	require.Equal(t, map[string]string{"1": "remove", "2": "change", "3": "add"}, decodeData(t, data))

	// Schema diff: cb has an email field ca lacks.
	schemaText, err := runCmd(t, quietDiff(cfg), "a", "b", "--schema")
	require.ErrorIs(t, err, errQuietExit)
	require.Contains(t, schemaText, "# schema")
	require.Contains(t, schemaText, ".email")
}

// TestDiffSchemaCrossDriverMongoIntegration pins that --schema now compares two
// different drivers: a connection-free file dump against a live MongoDB source.
// The mongo value carries an _id field the file value lacks, so the shapes differ
// — a legible cross-driver row rather than the old same-driver rejection.
func TestDiffSchemaCrossDriverMongoIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable MongoDB")
	}
	base := mongoBaseURL()
	seedMongo(t, base, "sc", []string{`{"_id":"1","name":"a","age":30}`})

	dump := filepath.Join(t.TempDir(), "d.jsonl")
	require.NoError(t, os.WriteFile(dump, []byte(`{"key":"1","value":{"name":"a","age":30}}`+"\n"), 0o600))

	c := newSeed()
	require.NoError(t, c.Add("f", "file://"+dump))
	require.NoError(t, c.Add("m", base+"?collection=sc"))
	seedConfig(t, c)

	cfg := &config{timeout: 8 * time.Second}
	out, err := runCmd(t, quietDiff(cfg), "f", "m", "--schema")
	// Allowed across drivers (no same-driver rejection); the shapes differ on _id,
	// so diff exits with the quiet-exit sentinel and renders the schema section.
	require.ErrorIs(t, err, errQuietExit)
	require.NotContains(t, out, "same driver")
	require.Contains(t, out, "# schema")
	require.Contains(t, out, "_id")
}

// TestReadAllReportsPagesRedisIntegration pins readAll's onPage contract: a
// non-nil callback receives one tick per scanned page whose sizes sum to the
// keyspace, and a nil callback is simply skipped. Without the non-nil case the
// `if onPage != nil` guard reads as dead (a mutant dropping the tick survives);
// the nil case guards against the inverse mutant that would call through nil.
func TestReadAllReportsPagesRedisIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable Redis")
	}
	base := redisBaseURL()
	u, err := withRedisDB(base, testRedisDB)
	require.NoError(t, err)
	kv := map[string]string{"k1": "v1", "k2": "v2", "k3": "v3"}
	seedRedis(t, u, kv)
	tgt := diffTarget{handle: "a", url: u}

	tests := []struct {
		name    string
		withCB  bool
		wantSum int
	}{
		{name: "non-nil onPage ticks per page", withCB: true, wantSum: len(kv)},
		{name: "nil onPage is skipped", withCB: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var pages []int
			var onPage func(int)
			if tt.withCB {
				onPage = func(n int) { pages = append(pages, n) }
			}
			got, err := readAll(ctx, tgt, onPage)
			require.NoError(t, err)
			require.Len(t, got, len(kv))
			if !tt.withCB {
				return
			}
			require.NotEmpty(t, pages)
			sum := 0
			for _, n := range pages {
				sum += n
			}
			require.Equal(t, tt.wantSum, sum)
		})
	}
}

// decodeData parses --json diff output into key -> op name for the data layer.
func decodeData(t *testing.T, out string) map[string]string {
	t.Helper()
	var parsed struct {
		Data []struct {
			Key string `json:"key"`
			Op  string `json:"op"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	got := make(map[string]string, len(parsed.Data))
	for _, d := range parsed.Data {
		got[d.Key] = d.Op
	}
	return got
}

// Integration tests operate on reserved Redis databases so a run never flushes
// DB 0, which developers seed (scripts/seed-redis.sh) for manual exploration. diff
// needs two sources, hence two reserved databases.
const (
	testRedisDB    = 15 // primary scratch database
	testRedisDBAlt = 14 // second scratch database, for diffing two sources
)

func redisBaseURL() string {
	if u := os.Getenv("IQ_REDIS_URL"); u != "" {
		return u
	}
	return "redis://localhost:6379/0"
}

func mongoBaseURL() string {
	if u := os.Getenv("IQ_MONGO_URL"); u != "" {
		return u
	}
	return "mongodb://localhost:27017/iq"
}

// withRedisDB rewrites the database number in a redis URL's path.
func withRedisDB(raw string, db int) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse redis url: %w", err)
	}
	u.Path = "/" + strconv.Itoa(db)
	return u.String(), nil
}

// seedRedis flushes the target DB and sets each key.
func seedRedis(t *testing.T, u string, kv map[string]string) {
	t.Helper()
	ctx := context.Background()
	st, err := openStore(ctx, &config{url: u})
	require.NoError(t, err)
	defer func() { _ = st.Close() }()
	r := query.NewRunner(st)
	_, err = r.Run(ctx, []string{"FLUSHDB"})
	require.NoError(t, err)
	for k, v := range kv {
		_, err = r.Run(ctx, []string{"SET", k, v})
		require.NoError(t, err)
	}
}

// seedMongo drops the collection and inserts each JSON document.
func seedMongo(t *testing.T, u, coll string, docs []string) {
	t.Helper()
	ctx := context.Background()
	st, err := openStore(ctx, &config{url: u, address: coll})
	require.NoError(t, err)
	defer func() { _ = st.Close() }()
	r := query.NewRunner(st)
	_, _ = r.Run(ctx, []string{fmt.Sprintf(`{"drop":%q}`, coll)}) // ignore "ns not found"
	for _, d := range docs {
		_, err = r.Run(ctx, []string{fmt.Sprintf(`{"insert":%q,"documents":[%s]}`, coll, d)})
		require.NoError(t, err)
	}
}

func TestLayerCount(t *testing.T) {
	tests := []struct {
		name                            string
		dataMode, statsMode, schemaMode bool
		want                            int
	}{
		{"none", false, false, false, 0},
		{"data only", true, false, false, 1},
		{"stats only", false, true, false, 1},
		{"schema only", false, false, true, 1},
		{"data and stats", true, true, false, 2},
		{"all three", true, true, true, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, layerCount(tt.dataMode, tt.statsMode, tt.schemaMode))
		})
	}
}

func TestRenderPatch(t *testing.T) {
	t.Run("non-empty patch prints and signals the quiet exit", func(t *testing.T) {
		var buf bytes.Buffer
		err := renderPatch(&buf, json.RawMessage(`[{"op":"add","path":"/x","value":1}]`))
		require.ErrorIs(t, err, errQuietExit)
		require.Contains(t, buf.String(), `"op": "add"`)
		require.Contains(t, buf.String(), `"path": "/x"`)
	})
	t.Run("empty patch prints [] and exits zero", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, renderPatch(&buf, json.RawMessage(`[]`)))
		require.Equal(t, "[]\n", buf.String())
	})
	t.Run("malformed patch is a decode error", func(t *testing.T) {
		var buf bytes.Buffer
		err := renderPatch(&buf, json.RawMessage(`{not a patch`))
		require.ErrorContains(t, err, "decode json patch")
		// %w keeps the json cause reachable for errors.As; %v would sever it.
		var syn *json.SyntaxError
		require.ErrorAs(t, err, &syn)
	})
	t.Run("write error propagates", func(t *testing.T) {
		err := renderPatch(&errAfter{0}, json.RawMessage(`[{"op":"add","path":"/x","value":1}]`))
		require.Error(t, err)
		// The write error itself, not the quiet-exit sentinel a dropped guard
		// would fall through to.
		require.NotErrorIs(t, err, errQuietExit)
	})
}

func TestDiffPatchRejectsMultipleLayers(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("a", "redis://h:6379/0"))
	require.NoError(t, c.Add("b", "redis://h:6379/1"))
	seedConfig(t, c)

	cfg := &config{timeout: time.Second}
	// Two layers with --patch is rejected before any store is opened, so no
	// connection is attempted.
	_, err := runCmd(t, quietDiff(cfg), "a", "b", "--data", "--schema", "--patch")
	require.ErrorContains(t, err, "single layer")
}

func TestDiffPatchExcludesStructuredFlags(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("a", "redis://h:6379/0"))
	require.NoError(t, c.Add("b", "redis://h:6379/1"))
	seedConfig(t, c)

	tests := []struct {
		name string
		flag string
	}{
		{"json", "--json"},
		{"yaml", "--yaml"},
		{"set-arrays", "--set-arrays"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config{timeout: time.Second}
			// Cobra enforces the flag groups before RunE, so the pair is rejected
			// without opening a store.
			_, err := runCmd(t, quietDiff(cfg), "a", "b", "--patch", tt.flag)
			require.ErrorContains(t, err, "none of the others can be")
		})
	}
}

// fileSource writes a one-item jsonl dump and returns its file:// URL, giving
// the patch and schema paths a connection-free end-to-end source.
func fileSource(t *testing.T, line string) string {
	t.Helper()
	dump := filepath.Join(t.TempDir(), "d.jsonl")
	require.NoError(t, os.WriteFile(dump, []byte(line+"\n"), 0o600))
	return "file://" + dump
}

func TestDiffPatchSingleLayer(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("a", fileSource(t, `{"key":"1","value":{"name":"a"}}`)))
	// b differs in value (for --data) and carries an extra field so the inferred
	// shape differs too (for --schema).
	require.NoError(t, c.Add("b", fileSource(t, `{"key":"1","value":{"name":"b","extra":true}}`)))
	seedConfig(t, c)

	for _, layer := range []string{"--data", "--schema"} {
		t.Run(layer, func(t *testing.T) {
			cfg := &config{timeout: 5 * time.Second}
			// Exactly one explicit layer passes the single-layer gate and renders
			// a real RFC 6902 patch; the differing sources signal the quiet exit,
			// never the layer-gate message.
			out, err := runCmd(t, quietDiff(cfg), "a", "b", layer, "--patch")
			require.ErrorIs(t, err, errQuietExit)
			require.NotContains(t, out, "single layer")
			var ops []map[string]any
			require.NoError(t, json.Unmarshal([]byte(out), &ops))
			require.NotEmpty(t, ops)
			for _, op := range ops {
				require.Contains(t, op, "op")
				require.Contains(t, op, "path")
			}
		})
	}

	t.Run("identical sources print the empty patch and exit zero", func(t *testing.T) {
		cfg := &config{timeout: 5 * time.Second}
		out, err := runCmd(t, quietDiff(cfg), "a", "a", "--data", "--patch")
		require.NoError(t, err)
		require.Equal(t, "[]\n", out)
	})
}

func TestDiffPatchStatsCrossDriverError(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("f", fileSource(t, `{"key":"1","value":{}}`)))
	require.NoError(t, c.Add("r", "redis://127.0.0.1:1/0"))
	seedConfig(t, c)

	cfg := &config{timeout: time.Second}
	// statsTrees rejects the cross-driver pair before dialing; the patch path
	// must surface that error, not fall through to decoding an absent patch.
	_, err := runCmd(t, quietDiff(cfg), "f", "r", "--stats", "--patch")
	require.ErrorContains(t, err, "same driver")
}

func TestDiffSchemaSelfIsEmpty(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("a", fileSource(t, `{"key":"1","value":{"name":"a","age":30}}`)))
	seedConfig(t, c)

	cfg := &config{timeout: 5 * time.Second}
	// A source diffed against itself has identical shapes on both sides: exit
	// zero. A collector that loses a side would report every field as a delta.
	out, err := runCmd(t, newDiffCmd(cfg), "a", "a", "--schema")
	require.NoError(t, err)
	require.Contains(t, out, "no differences")
}

func TestDiffCombinedLayersAllowed(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("a", fileSource(t, `{"key":"1","value":{"name":"a"}}`)))
	seedConfig(t, c)

	cfg := &config{timeout: 5 * time.Second}
	// Two layers without --patch combine freely; only --patch demands a single
	// layer. The self-diff is empty on both layers, so the run exits zero.
	out, err := runCmd(t, newDiffCmd(cfg), "a", "a", "--data", "--schema")
	require.NoError(t, err)
	require.Contains(t, out, "no differences")
}

func TestPatchLayerCanceledContext(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("a", "redis://127.0.0.1:1/0"))
	require.NoError(t, c.Add("b", "redis://127.0.0.1:1/1"))
	seedConfig(t, c)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	left, err := resolveDiffTarget(cf, "a")
	require.NoError(t, err)
	right, err := resolveDiffTarget(cf, "b")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name                  string
		statsMode, schemaMode bool
	}{
		{"data layer", false, false},
		{"stats layer", true, false},
		{"schema layer", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The canceled context fails each arm's collector before any dial; an
			// arm that drops the context would reach the network and fail
			// differently.
			_, err := patchLayer(ctx, left, right, tt.statsMode, tt.schemaMode, nil, 0, nil)
			require.ErrorIs(t, err, context.Canceled)
		})
	}
}

func TestDiffStatsSelfSectionRedisIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable Redis")
	}
	base := redisBaseURL()
	urlA, err := withRedisDB(base, testRedisDB)
	require.NoError(t, err)

	c := newSeed()
	require.NoError(t, c.Add("a", urlA))
	seedConfig(t, c)

	cfg := &config{timeout: 5 * time.Second}
	// The cluster section is a single stable field (cluster_enabled:0), so a
	// self-diff is deterministically empty and exits zero. A collector that
	// loses either side would report the whole section as added or removed.
	out, err := runCmd(t, newDiffCmd(cfg), "a", "a", "--stats", "--section", "cluster")
	require.NoError(t, err)
	require.Contains(t, out, "no differences")
}

func TestDiffStatsCanceledContext(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("a", "redis://127.0.0.1:1/0"))
	require.NoError(t, c.Add("b", "redis://127.0.0.1:1/1"))
	seedConfig(t, c)
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	left, err := resolveDiffTarget(cf, "a")
	require.NoError(t, err)
	right, err := resolveDiffTarget(cf, "b")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The already-canceled context fails the collectors before any dial; a
	// collector that drops the context would reach the network and fail
	// differently.
	_, err = diffStats(ctx, left, right, nil, diff.Options{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestDiffPatchRedisIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable Redis")
	}
	base := redisBaseURL()
	urlA, err := withRedisDB(base, testRedisDB)
	require.NoError(t, err)
	urlB, err := withRedisDB(base, testRedisDBAlt)
	require.NoError(t, err)
	seedRedis(t, urlA, map[string]string{"keep": "same", "shared": "old"})
	seedRedis(t, urlB, map[string]string{"keep": "same", "shared": "new", "extra": "add"})

	c := newSeed()
	require.NoError(t, c.Add("a", urlA))
	require.NoError(t, c.Add("b", urlB))
	seedConfig(t, c)

	cfg := &config{timeout: 5 * time.Second}

	// Differing sources: the patch is non-empty and diff exits with the quiet-exit
	// sentinel while still printing a valid RFC 6902 document.
	out, err := runCmd(t, quietDiff(cfg), "a", "b", "--patch")
	require.ErrorIs(t, err, errQuietExit)
	var ops []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &ops))
	require.NotEmpty(t, ops)
	for _, op := range ops {
		require.Contains(t, []any{"add", "remove", "replace"}, op["op"])
	}

	// Identical sources: the patch is the literal empty array and diff exits zero.
	same, err := runCmd(t, newDiffCmd(cfg), "a", "a", "--patch")
	require.NoError(t, err)
	require.Equal(t, "[]\n", same)
}

// TestDiffSetArraysMongoIntegration pins that --set-arrays threads into the data
// layer: a document whose only difference is a reordered array reads as changed by
// default but equal under --set-arrays.
func TestDiffSetArraysMongoIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable MongoDB")
	}
	base := mongoBaseURL()
	seedMongo(t, base, "sa", []string{`{"_id":"1","tags":["a","b","c"]}`})
	seedMongo(t, base, "sb", []string{`{"_id":"1","tags":["c","b","a"]}`})

	c := newSeed()
	require.NoError(t, c.Add("a", base+"?collection=sa"))
	require.NoError(t, c.Add("b", base+"?collection=sb"))
	seedConfig(t, c)

	cfg := &config{timeout: 8 * time.Second}

	// Default order-sensitive diff: the reordered array makes the item differ.
	_, err := runCmd(t, quietDiff(cfg), "a", "b", "--data")
	require.ErrorIs(t, err, errQuietExit)

	// --set-arrays compares the array as a multiset, so the reorder is no delta and
	// diff exits zero.
	_, err = runCmd(t, newDiffCmd(cfg), "a", "b", "--data", "--set-arrays")
	require.NoError(t, err)
}

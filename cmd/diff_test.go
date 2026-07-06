package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
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
	require.NoError(t, c.Add("known", "redis://h:6379/0", ""))
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
		diffTarget{handle: "b", driver: "mongo"}, nil)
	require.ErrorContains(t, err, "same driver")
}

func TestDiffSchemaRejectsCrossDriver(t *testing.T) {
	_, err := diffSchema(context.Background(),
		diffTarget{handle: "a", driver: "mongo"},
		diffTarget{handle: "b", driver: "redis"}, 100)
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
	require.NoError(t, c.Add("a", urlA, ""))
	require.NoError(t, c.Add("b", urlB, ""))
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
	require.NoError(t, c.Add("a", urlA, ""))
	require.NoError(t, c.Add("b", urlB, ""))
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
	require.NoError(t, c.Add("a", base, "ca"))
	require.NoError(t, c.Add("b", base, "cb"))
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
// DB 0, which developers seed (scripts/seed.sh) for manual exploration. diff
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
	st, err := openStore(ctx, &config{url: u, collection: coll})
	require.NoError(t, err)
	defer func() { _ = st.Close() }()
	r := query.NewRunner(st)
	_, _ = r.Run(ctx, []string{fmt.Sprintf(`{"drop":%q}`, coll)}) // ignore "ns not found"
	for _, d := range docs {
		_, err = r.Run(ctx, []string{fmt.Sprintf(`{"insert":%q,"documents":[%s]}`, coll, d)})
		require.NoError(t, err)
	}
}

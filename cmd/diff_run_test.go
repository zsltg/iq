package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	iqfile "github.com/zsltg/iq/drivers/file"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
)

// seedDiffFiles seeds one file source per handle, each from its own jsonl lines.
func seedDiffFiles(t *testing.T, sources map[string][]string) {
	t.Helper()
	c := newSeed()
	for handle, lines := range sources {
		dump := filepath.Join(t.TempDir(), handle+".jsonl")
		body := ""
		for _, line := range lines {
			body += line + "\n"
		}
		require.NoError(t, os.WriteFile(dump, []byte(body), 0o600))
		require.NoError(t, c.Add(handle, iqfile.URL(dump)))
	}
	seedConfig(t, c)
}

// TestDiffValidationOrder pins the order of the checks in the diff command:
// load the config, parse the left side, parse the right side, check --patch
// against the layer count, then check --stats against a filter. Each row trips
// two checks, and only the first one reports.
func TestDiffValidationOrder(t *testing.T) {
	const (
		patchLayerErr  = "--patch renders one JSON Patch, so it needs a single layer; choose exactly one of --data, --stats, --schema"
		statsFilterErr = "--stats diffs the backend's own introspection, which has no items to filter; drop the filter or diff --data/--schema"
	)
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"the left side parses before the right side", []string{"ghost1", "ghost2"}, "unknown source \"ghost1\"; run `iq ls` (register a dump with `iq add file:///path/to/dump.json` to read one)"},
		{"the right side parses before the patch check", []string{"a", "ghost", "--data", "--schema", "--patch"}, "unknown source \"ghost\"; run `iq ls` (register a dump with `iq add file:///path/to/dump.json` to read one)"},
		{"the patch check comes before the stats filter check", []string{"a=.[]", "b", "--stats", "--schema", "--patch"}, patchLayerErr},
		{"the stats filter check", []string{"a", "b=.[]", "--stats"}, statsFilterErr},
		{"the stats filter check on the left side", []string{"a=.[]", "b", "--stats"}, statsFilterErr},
		{"the stats filter check on the filter flag", []string{"a", "b", "--stats", "--filter", ".[]"}, statsFilterErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedDiffFiles(t, map[string][]string{"a": {`{"key":"1","value":{}}`}, "b": {`{"key":"1","value":{}}`}})

			_, err := runCmd(t, quietDiff(&config{timeout: 5 * time.Second}), tt.args...)

			require.EqualError(t, err, tt.wantErr)
		})
	}
}

// TestDiffReadsLeftBeforeRight pins the read order: when both sides fail, the
// error names the left side, on every layer and on the patch path.
func TestDiffReadsLeftBeforeRight(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("l", "file:///nonexistent-iq-test/l.jsonl"))
	require.NoError(t, c.Add("r", "file:///nonexistent-iq-test/r.jsonl"))
	seedConfig(t, c)
	// The text of a missing-file error depends on the OS, so take it from the OS.
	const leftDump = "/nonexistent-iq-test/l.jsonl"
	_, openErr := os.Open(leftDump)
	require.Error(t, openErr)

	for _, layer := range [][]string{{"--data"}, {"--schema"}, {"--stats"}, {"--data", "--patch"}, {"--schema", "--patch"}, {"--stats", "--patch"}} {
		t.Run(strings.Join(layer, " "), func(t *testing.T) {
			args := append([]string{"l", "r"}, layer...)

			_, err := runCmd(t, quietDiff(&config{timeout: 5 * time.Second}), args...)

			require.EqualError(t, err, fmt.Sprintf("open dump %q: %v", leftDump, openErr))
		})
	}
}

// TestDiffOutputAndQuietExit pins the rendered delta and the exit contract of
// the human path: the sources differ, so the output renders in full and the run
// returns errQuietExit.
func TestDiffOutputAndQuietExit(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"text", nil, "a (file)  →  b (file)\n\n# data\n~ 1\n    ~ name: \"a\" → \"b\"\n0 added, 0 removed, 1 changed\n\n"},
		{
			"json",
			[]string{"--json"},
			"{\n  \"data\": [\n    {\n      \"key\": \"1\",\n      \"op\": \"change\",\n      \"changes\": [\n        {\n" +
				"          \"path\": [\n            \"name\"\n          ],\n          \"op\": \"change\",\n" +
				"          \"old\": \"a\",\n          \"new\": \"b\"\n        }\n      ]\n    }\n  ]\n}\n",
		},
		{
			"yaml",
			[]string{"--yaml"},
			"data:\n    - key: \"1\"\n      op: 2\n      changes:\n        - path:\n            - name\n          op: 2\n" +
				"          old: a\n          new: b\n      old: null\n      new: null\nstats: []\nschema: []\n",
		},
		{"patch with no layer is the data layer", []string{"--patch"}, "[\n  {\n    \"op\": \"replace\",\n    \"path\": \"/1/name\",\n    \"value\": \"b\"\n  }\n]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedDiffFiles(t, map[string][]string{
				"a": {`{"key":"1","value":{"name":"a"}}`},
				"b": {`{"key":"1","value":{"name":"b"}}`},
			})
			args := append([]string{"a", "b"}, tt.args...)

			out, err := runCmd(t, quietDiff(&config{timeout: 5 * time.Second}), args...)

			require.ErrorIs(t, err, errQuietExit)
			require.Equal(t, tt.want, stripANSI(out))
		})
	}
}

// TestDiffOptionsReachTheLayers proves that --set-arrays, --sample, and
// --section reach the layer that uses them.
func TestDiffOptionsReachTheLayers(t *testing.T) {
	const unknownSection = `unknown inspect subcommand "bogus"; want one of dbStats, serverStatus, listCollections, collStats, buildInfo, hostInfo`
	tests := []struct {
		name    string
		args    []string
		wantErr string // "" means no error; "quiet" means errQuietExit
	}{
		{"arrays in another order differ", []string{"arr1", "arr2", "--data"}, "quiet"},
		{"set-arrays ignores the order", []string{"arr1", "arr2", "--data", "--set-arrays"}, ""},
		{"a full sample sees the second type", []string{"wide=.[] | .tags[]", "narrow=.[] | .tags[]", "--schema"}, "quiet"},
		{"a sample of one does not see the second type", []string{"wide=.[] | .tags[]", "narrow=.[] | .tags[]", "--schema", "--sample", "1"}, ""},
		{"a patch sample of one does not see the second type", []string{"wide=.[] | .tags[]", "narrow=.[] | .tags[]", "--schema", "--sample", "1", "--patch"}, ""},
		{"a stats section reaches the collector", []string{"arr1", "arr2", "--stats", "--section", "bogus"}, unknownSection},
		{"a patch stats section reaches the collector", []string{"arr1", "arr2", "--stats", "--section", "bogus", "--patch"}, unknownSection},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedDiffFiles(t, map[string][]string{
				"arr1":   {`{"key":"1","value":{"tags":["x","y"]}}`},
				"arr2":   {`{"key":"1","value":{"tags":["y","x"]}}`},
				"wide":   {`{"key":"1","value":{"tags":["s",1]}}`},
				"narrow": {`{"key":"1","value":{"tags":["s"]}}`},
			})

			_, err := runCmd(t, quietDiff(&config{timeout: 5 * time.Second}), tt.args...)

			switch tt.wantErr {
			case "":
				require.NoError(t, err)
			case "quiet":
				require.ErrorIs(t, err, errQuietExit)
			default:
				require.EqualError(t, err, tt.wantErr)
			}
		})
	}
}

// TestDiffCommandForwardsContext proves that every layer, on the human path and
// on the patch path, reads under the context of the command.
func TestDiffCommandForwardsContext(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("a", "redis://127.0.0.1:1/0"))
	require.NoError(t, c.Add("b", "redis://127.0.0.1:1/1"))
	seedConfig(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, layer := range [][]string{{"--data"}, {"--stats"}, {"--schema"}, {"--data", "--patch"}, {"--stats", "--patch"}, {"--schema", "--patch"}} {
		t.Run(strings.Join(layer, " "), func(t *testing.T) {
			dc := quietDiff(&config{timeout: 5 * time.Second})
			dc.SetOut(io.Discard)
			dc.SetErr(io.Discard)
			dc.SetArgs(append([]string{"a", "b"}, layer...))

			err := dc.ExecuteContext(ctx)

			require.ErrorIs(t, err, context.Canceled)
		})
	}
}

// TestDiffCommandAppliesTimeout proves that every layer, on the human path and
// on the patch path, reads under the --timeout deadline.
func TestDiffCommandAppliesTimeout(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("a", "redis://127.0.0.1:1/0"))
	require.NoError(t, c.Add("b", "redis://127.0.0.1:1/1"))
	seedConfig(t, c)

	for _, layer := range [][]string{{"--data"}, {"--stats"}, {"--schema"}, {"--data", "--patch"}, {"--stats", "--patch"}, {"--schema", "--patch"}} {
		t.Run(strings.Join(layer, " "), func(t *testing.T) {
			_, err := runCmd(t, quietDiff(&config{timeout: time.Nanosecond}), append([]string{"a", "b"}, layer...)...)

			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

// TestDiffStatsCrossDriverText pins the exact text of the cross-driver error.
// The check runs before either side opens, so the missing dump of the left side
// does not report.
func TestDiffStatsCrossDriverText(t *testing.T) {
	const want = `stats diff needs two sources of the same driver; "f" is file and "r" is redis`
	c := newSeed()
	require.NoError(t, c.Add("f", "file:///nonexistent-iq-test/f.jsonl"))
	require.NoError(t, c.Add("r", "redis://127.0.0.1:1/0"))
	seedConfig(t, c)

	for _, extra := range [][]string{{"--stats"}, {"--stats", "--patch"}} {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			_, err := runCmd(t, quietDiff(&config{timeout: 5 * time.Second}), append([]string{"f", "r"}, extra...)...)

			require.EqualError(t, err, want)
		})
	}
}

// ctxRecorder records the context of each open and each read, by source URI.
// It also records each raw query and each scan page that a read takes. pages
// sets how many one-item pages a scan offers; zero means one. replies sets the
// raw query reply for a source URI; no entry means {"ok": 1}.
type ctxRecorder struct {
	mu      sync.Mutex
	ctxs    map[string][]context.Context
	queries map[string][]string
	scanned map[string]int
	pages   int
	replies map[string]any
}

func (r *ctxRecorder) query(url, q string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries[url] = append(r.queries[url], q)
}

func (r *ctxRecorder) page(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanned[url]++
}

func (r *ctxRecorder) add(url string, ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctxs[url] = append(r.ctxs[url], ctx)
}

// ctxRecordingStore is a store with one item. Each scan and each raw query
// records its context.
type ctxRecordingStore struct {
	fakeInspectStore
	rec *ctxRecorder
	url string
}

func (s ctxRecordingStore) ScanBatches(ctx context.Context, fn func(map[string]any) error) error {
	s.rec.add(s.url, ctx)
	for i := range max(s.rec.pages, 1) {
		s.rec.page(s.url)
		if err := fn(map[string]any{strconv.Itoa(i + 1): map[string]any{"n": 1.0}}); err != nil {
			return err
		}
	}
	return nil
}

func (s ctxRecordingStore) Query(ctx context.Context, args []string) (any, error) {
	s.rec.add(s.url, ctx)
	s.rec.query(s.url, args[0])
	if reply, ok := s.rec.replies[s.url]; ok {
		return reply, nil
	}
	return map[string]any{"ok": 1.0}, nil
}

// useCtxDriver registers the ctxrec:// driver for the test, seeds the sources
// "a" and "b" on it, and returns the recorder.
func useCtxDriver(t *testing.T) *ctxRecorder {
	t.Helper()
	rec := &ctxRecorder{ctxs: map[string][]context.Context{}, queries: map[string][]string{}, scanned: map[string]int{}}
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	drivers = append(append([]driver{}, orig...), driver{
		name:    "ctxrec",
		schemes: []string{"ctxrec"},
		open: func(ctx context.Context, cfg *config) (store, error) {
			rec.add(cfg.url, ctx)
			return ctxRecordingStore{rec: rec, url: cfg.url}, nil
		},
	})
	c := newSeed()
	require.NoError(t, c.Add("a", "ctxrec://a"))
	require.NoError(t, c.Add("b", "ctxrec://b"))
	seedConfig(t, c)
	return rec
}

// requireSideContexts requires that each side opened and read at least once,
// and that each check passes for every recorded context.
func requireSideContexts(t *testing.T, rec *ctxRecorder, check func(t *testing.T, ctx context.Context)) {
	t.Helper()
	for _, url := range []string{"ctxrec://a", "ctxrec://b"} {
		require.GreaterOrEqual(t, len(rec.ctxs[url]), 2, "%s must open and read", url)
		for _, ctx := range rec.ctxs[url] {
			require.NotNil(t, ctx, url)
			check(t, ctx)
		}
	}
}

// diffCtxKey is the context key that marks the context of the caller.
type diffCtxKey struct{}

// TestDiffForwardsContextToBothSides proves that every layer, on the human path
// and on the patch path, opens and reads both sides under the context of the
// command, with the --timeout deadline.
func TestDiffForwardsContextToBothSides(t *testing.T) {
	for _, layer := range [][]string{{"--data"}, {"--stats"}, {"--schema"}, {"--data", "--patch"}, {"--stats", "--patch"}, {"--schema", "--patch"}} {
		t.Run(strings.Join(layer, " "), func(t *testing.T) {
			rec := useCtxDriver(t)
			dc := quietDiff(&config{timeout: time.Minute})
			dc.SetOut(io.Discard)
			dc.SetErr(io.Discard)
			dc.SetArgs(append([]string{"a", "b"}, layer...))

			err := dc.ExecuteContext(context.WithValue(context.Background(), diffCtxKey{}, "marker"))

			require.NoError(t, err)
			requireSideContexts(t, rec, func(t *testing.T, ctx context.Context) {
				require.Equal(t, "marker", ctx.Value(diffCtxKey{}))
				_, ok := ctx.Deadline()
				require.True(t, ok, "the read must run under the --timeout deadline")
			})
		})
	}
}

// TestDiffDataTicksBothSides proves that the data layer and the patch data layer
// tick the page callback once for each page of each side.
func TestDiffDataTicksBothSides(t *testing.T) {
	rec := useCtxDriver(t)
	// Two pages on each side give one callback for each page.
	rec.pages = 2
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	left, err := diffSpec(cf, "a")
	require.NoError(t, err)
	right, err := diffSpec(cf, "b")
	require.NoError(t, err)

	t.Run("data", func(t *testing.T) {
		var pages []int

		_, err := diffRun{cfg: &config{}, left: left, right: right}.diffData(t.Context(), func(n int) { pages = append(pages, n) })

		require.NoError(t, err)
		require.Equal(t, []int{1, 1, 1, 1}, pages)
	})

	t.Run("patch data", func(t *testing.T) {
		var pages []int

		_, err := diffRun{cfg: &config{}, left: left, right: right}.patchLayer(t.Context(), false, false, func(n int) { pages = append(pages, n) })

		require.NoError(t, err)
		require.Equal(t, []int{1, 1, 1, 1}, pages)
	})
}

// TestDiffRunGivesOptionsToBothSides proves that the stats layer runs the
// --section list on both sides, and that the schema layer samples both sides
// with --sample, on the human path and on the patch path.
func TestDiffRunGivesOptionsToBothSides(t *testing.T) {
	tests := []struct {
		name        string
		run         func(r diffRun) error
		wantQueries map[string][]string
		wantScanned map[string]int
	}{
		{
			"stats",
			func(r diffRun) error { _, err := r.diffStats(t.Context()); return err },
			map[string][]string{"ctxrec://a": {`{"buildInfo":1}`}, "ctxrec://b": {`{"buildInfo":1}`}},
			map[string]int{},
		},
		{
			"patch stats",
			func(r diffRun) error { _, err := r.patchLayer(t.Context(), true, false, nil); return err },
			map[string][]string{"ctxrec://a": {`{"buildInfo":1}`}, "ctxrec://b": {`{"buildInfo":1}`}},
			map[string]int{},
		},
		{
			"schema",
			func(r diffRun) error { _, err := r.diffSchema(t.Context()); return err },
			map[string][]string{},
			map[string]int{"ctxrec://a": 1, "ctxrec://b": 1},
		},
		{
			"patch schema",
			func(r diffRun) error { _, err := r.patchLayer(t.Context(), false, true, nil); return err },
			map[string][]string{},
			map[string]int{"ctxrec://a": 1, "ctxrec://b": 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := useCtxDriver(t)
			rec.pages = 2
			cf, err := iqconfig.Load()
			require.NoError(t, err)
			left, err := diffSpec(cf, "a")
			require.NoError(t, err)
			right, err := diffSpec(cf, "b")
			require.NoError(t, err)
			r := diffRun{cfg: &config{}, left: left, right: right, sections: []string{"buildInfo"}, sample: 1}

			require.NoError(t, tt.run(r))

			require.Equal(t, tt.wantQueries, rec.queries)
			require.Equal(t, tt.wantScanned, rec.scanned)
		})
	}
}

// TestDiffStatsReportsTheChange proves that the stats layer puts its changes in
// the report. Different introspection replies give the exact change and
// errQuietExit. Equal replies give no change and no error.
func TestDiffStatsReportsTheChange(t *testing.T) {
	tests := []struct {
		name    string
		replyB  any
		want    string
		wantErr error
	}{
		{
			"a changed reply",
			map[string]any{"version": "8.0"},
			"{\n  \"stats\": [\n    {\n      \"path\": [\n        \"buildInfo\",\n        \"version\"\n      ],\n" +
				"      \"op\": \"change\",\n      \"old\": \"7.0\",\n      \"new\": \"8.0\"\n    }\n  ]\n}\n",
			errQuietExit,
		},
		{
			"an equal reply",
			map[string]any{"version": "7.0"},
			"{}\n",
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := useCtxDriver(t)
			rec.replies = map[string]any{"ctxrec://a": map[string]any{"version": "7.0"}, "ctxrec://b": tt.replyB}

			out, err := runCmd(t, quietDiff(&config{timeout: time.Minute}), "a", "b", "--stats", "--section", "buildInfo", "--json")

			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}
			require.Equal(t, tt.want, out)
		})
	}
}

// TestDiffFirstFailingLayerWins pins the layer order: the stats layer runs
// before the schema layer, and its error returns unchanged. A run that went on
// to the schema layer would replace it with no error.
func TestDiffFirstFailingLayerWins(t *testing.T) {
	const unknownSection = `unknown inspect subcommand "bogus"; want one of dbStats, serverStatus, listCollections, collStats, buildInfo, hostInfo`
	seedDiffFiles(t, map[string][]string{"a": {`{"key":"1","value":{}}`}, "b": {`{"key":"1","value":{}}`}})

	_, err := runCmd(t, quietDiff(&config{timeout: 5 * time.Second}), "a", "b", "--stats", "--schema", "--section", "bogus")

	require.EqualError(t, err, unknownSection)
}

// TestDiffStatsSectionLoop pins the Mongo section loop. It checks each section
// inside the read loop, so a valid section before an unknown one reads first.
// The collStats section needs a collection and is skipped without one.
func TestDiffStatsSectionLoop(t *testing.T) {
	seedDiffFiles(t, map[string][]string{"a": {`{"key":"1","value":{}}`}, "b": {`{"key":"1","value":{}}`}})

	t.Run("a valid section reads before an unknown one", func(t *testing.T) {
		_, err := runCmd(t, quietDiff(&config{timeout: 5 * time.Second}), "a", "b", "--stats", "--section", "dbStats,bogus")

		require.ErrorContains(t, err, `inspect "a" dbStats: `)
		require.NotContains(t, err.Error(), "unknown inspect subcommand")
	})

	t.Run("collStats without a collection is skipped", func(t *testing.T) {
		out, err := runCmd(t, quietDiff(&config{timeout: 5 * time.Second}), "a", "b", "--stats", "--section", "collStats")

		require.NoError(t, err)
		require.Equal(t, "a (file)  →  b (file)\n\n# stats\nno differences\n\n", stripANSI(out))
	})
}

// TestSampleItemsPassesRunOptions proves that the filtered sample reads under
// the run options of the caller: the logger of the options receives the
// scan strategy record.
func TestSampleItemsPassesRunOptions(t *testing.T) {
	path := writeJSONL(t, `{"key":"1","value":{"a":1}}`+"\n")
	st, err := openStore(t.Context(), &config{url: iqfile.URL(path)})
	require.NoError(t, err)
	defer func() { _ = st.Close() }()
	var buf strings.Builder
	opts := query.RunOptions{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}

	_, err = sampleItems(t.Context(), st, ".[]", 0, opts)

	require.NoError(t, err)
	require.Contains(t, buf.String(), "scan strategy")
}

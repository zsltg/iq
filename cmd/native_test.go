package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	iqfile "github.com/zsltg/iq/drivers/file"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// nativeExport is a DynamoDB export with a fractional number, a number above int64,
// a binary value, and a set: the types that the normalized form loses.
const nativeExport = `{"Item":{"pk":{"S":"a"},"price":{"N":"1.50"},"big":{"N":"12345678901234567890"},"raw":{"B":"AAH/"},"tags":{"SS":["y","x"]}}}
{"Item":{"pk":{"S":"b"},"price":{"N":"2"}}}
`

// seedNativeSources registers the DynamoDB export as "ddb", a typed dump as
// "snap", and the destination "dst" for useRecordingDestination.
func seedNativeSources(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	export := filepath.Join(dir, "export.json")
	require.NoError(t, os.WriteFile(export, []byte(nativeExport), 0o600))
	dump := filepath.Join(dir, "dump.jsonl")
	require.NoError(t, os.WriteFile(dump, []byte(moveDump), 0o600))
	c := newSeed()
	require.NoError(t, c.Add("ddb", iqfile.URL(export)+"?format=dynamodb-json&keys=pk"))
	require.NoError(t, c.Add("snap", iqfile.URL(dump)))
	require.NoError(t, c.Add("dst", "recput://h"))
	seedConfig(t, c)
}

func TestNativeQuery(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"keeps the number text", []string{"--native", "--src", "ddb", ".[] | .price.N", "--jsonl"}, "\"1.50\"\n\"2\"\n"},
		{"keeps a huge number", []string{"--native", "--src", "ddb", `.[] | select(.pk.S == "a") | .big.N`, "--jsonl"}, "\"12345678901234567890\"\n"},
		{"keeps binary and sets", []string{"--native", "--src", "ddb", `.[] | select(.pk.S == "a") | [.raw, .tags]`, "--jsonl"}, `[{"B":"AAH/"},{"SS":["y","x"]}]` + "\n"},
		{"yaml", []string{"--native", "--src", "ddb", `.[] | select(.pk.S == "b") | .price`, "--yaml"}, "\"N\": \"2\"\n"},
		{"without the flag the value is normalized", []string{"--src", "ddb", ".[] | .price", "--jsonl"}, "\"1.50\"\n2\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedNativeSources(t)
			root, _ := newRootCmd()
			out, err := runCmd(t, root, tt.args...)
			require.NoError(t, err)
			require.Equal(t, tt.want, out)
		})
	}
}

func TestNativeTypedDump(t *testing.T) {
	seedNativeSources(t)
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "--native", "--typed", "--src", "ddb")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.ElementsMatch(t, []string{
		`{"key":"a","type":"item","form":"dynamodb-json","value":{"big":{"N":"12345678901234567890"},"pk":{"S":"a"},"price":{"N":"1.50"},"raw":{"B":"AAH/"},"tags":{"SS":["y","x"]}}}`,
		`{"key":"b","type":"item","form":"dynamodb-json","value":{"pk":{"S":"b"},"price":{"N":"2"}}}`,
	}, lines)
}

func TestTypedDumpWithoutNativeHasNoForm(t *testing.T) {
	seedNativeSources(t)
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "--typed", "--src", "ddb")
	require.NoError(t, err)
	require.NotContains(t, out, `"form"`)
}

func TestNativeTypedDumpYAML(t *testing.T) {
	seedNativeSources(t)
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "--native", "--typed", "--yaml", "--src", "ddb")
	require.NoError(t, err)
	require.Contains(t, out, "form: dynamodb-json")
}

// TestNativeDumpReadsBack proves the dump round trip: a native typed dump read
// as a source gives the same native records.
func TestNativeDumpReadsBack(t *testing.T) {
	seedNativeSources(t)
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "--native", "--typed", "--src", "ddb")
	require.NoError(t, err)

	src := query.JSONLSource(strings.NewReader(out), 10, false)
	var got []query.Record
	require.NoError(t, src(context.Background(), func(b []query.Record) error {
		got = append(got, b...)
		return nil
	}))
	require.Len(t, got, 2)
	for _, r := range got {
		require.Equal(t, query.FormDynamoJSON, r.Form)
	}
}

func TestNativeRefusals(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"raw", []string{"--native", "--src", "ddb", ".[]", "--raw"}, "--native cannot use --format values"},
		{"gron", []string{"--native", "--src", "ddb", ".[]", "--gron"}, "--native cannot use --format gron"},
		{"grona", []string{"--native", "--src", "ddb", ".[]", "--grona"}, "--native cannot use --format grona"},
		{"parquet", []string{"--native", "--src", "ddb", ".[]", "--format", "parquet"}, "--native cannot use --format parquet"},
		{"source function", []string{"--native", `source("ddb"; ".[]")`}, "--native cannot read through source()"},
		{"foreign json", []string{"--native", "--src", "ddb", "--typed", "--key-field", "id"}, "--native cannot read foreign JSON"},
		{"source without a native form", []string{"--native", "--src", "snap", ".[]"}, "has no native form"},
		{"named keys", []string{"--native", "--src", "ddb", `.["a"]`}, "cannot fetch named keys"},
		{"typed dump of a source without a native form", []string{"--native", "--typed", "--src", "snap"}, "has no native form"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedNativeSources(t)
			root, _ := newRootCmd()
			_, err := runCmd(t, root, tt.args...)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestNativeExplainShowsNoPushdown(t *testing.T) {
	seedNativeSources(t)
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "--native", "--explain", "--src", "ddb", `.[] | select(.pk.S == "a")`)
	require.NoError(t, err)
	require.Contains(t, out, "native read:\n  read the native form of the source\n  push no predicate down\n")
	require.NotContains(t, out, "prefilter")

	root, _ = newRootCmd()
	out, err = runCmd(t, root, "--explain", "--src", "ddb", `.[] | select(.pk.S == "a")`)
	require.NoError(t, err)
	require.NotContains(t, out, "native form")
}

func TestNativeMoveExplain(t *testing.T) {
	seedNativeSources(t)
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "--native", "--typed", "--explain", "--src", "ddb")
	require.NoError(t, err)
	require.Contains(t, out, "records carry the native form of the source")
}

// recPutStore is a destination that records the batches it receives.
type recPutStore struct {
	fakeInspectStore
	mu      *sync.Mutex
	batches *[][]query.Record
	cleared *int
	own     query.Form
}

func (s recPutStore) WritesForm() query.Form { return s.own }

// Clear counts the calls, so a test can prove that --replace never ran.
func (s recPutStore) Clear(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.cleared++
	return nil
}

func (s recPutStore) Put(_ context.Context, batch []query.Record, _ query.WriteMode) (query.WriteStat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.batches = append(*s.batches, append([]query.Record(nil), batch...))
	return query.WriteStat{Written: len(batch)}, nil
}

// useRecordingDestination registers the scheme recput and the source "dst" on it.
func useRecordingDestination(t *testing.T, own query.Form) (*[][]query.Record, *int) {
	t.Helper()
	var mu sync.Mutex
	var batches [][]query.Record
	cleared := new(int)
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	drivers = append(append([]driver{}, orig...), driver{
		name: "recput", schemes: []string{"recput"},
		open: func(context.Context, *config) (store, error) {
			return recPutStore{mu: &mu, batches: &batches, cleared: cleared, own: own}, nil
		},
	})
	return &batches, cleared
}

func TestNativeInsertCarriesTheForm(t *testing.T) {
	seedNativeSources(t)
	batches, _ := useRecordingDestination(t, query.FormDynamoJSON)
	for _, native := range []bool{true, false} {
		*batches = nil
		root, _ := newRootCmd()
		args := []string{"--src", "ddb", "--insert", "dst"}
		if native {
			args = append(args, "--native")
		}
		_, err := runCmd(t, root, args...)
		require.NoError(t, err)
		require.Len(t, *batches, 1)
		want := query.FormJSON
		if native {
			want = query.FormDynamoJSON
		}
		for _, r := range (*batches)[0] {
			require.Equal(t, want, r.Form)
		}
	}
}

func TestMCPQueryNative(t *testing.T) {
	seedNativeSources(t)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	var out mcpQueryOutput
	structOf(t, callMCP(t, cs, "iq_query", map[string]any{
		"source": "ddb", "filter": ".[] | .price.N", "native": true,
	}), &out)
	require.ElementsMatch(t, []any{"1.50", "2"}, out.Items)

	body := errorBodyOf(t, callMCP(t, cs, "iq_query", map[string]any{
		"source": "snap", "filter": ".[]", "native": true,
	}))
	require.Contains(t, body.Error.Message, "native form")

	body = errorBodyOf(t, callMCP(t, cs, "iq_query", map[string]any{
		"filter": `source("ddb"; ".[]")`, "native": true,
	}))
	require.Contains(t, body.Error.Message, "--native cannot read through source()")
}

func TestMCPInsertNative(t *testing.T) {
	seedNativeSources(t)
	batches, _ := useRecordingDestination(t, query.FormDynamoJSON)
	cs := connectMCP(t, newTestMCPServer([]string{allowWrites}, 200, 256*1024), nil)

	var out mcpWriteOutput
	structOf(t, callMCP(t, cs, "iq_insert", map[string]any{
		"source": "ddb", "destination": "dst", "native": true,
	}), &out)
	require.Equal(t, 2, out.Written)
	require.Len(t, *batches, 1)
	for _, r := range (*batches)[0] {
		require.Equal(t, query.FormDynamoJSON, r.Form)
	}
}

func TestMCPToolsAdvertiseNative(t *testing.T) {
	seedNativeSources(t)
	cs := connectMCP(t, newTestMCPServer([]string{allowWrites}, 200, 256*1024), nil)
	for _, name := range []string{"iq_query", "iq_insert"} {
		tool := toolByName(t, cs, name)
		props, ok := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
		require.True(t, ok, name)
		require.Contains(t, props, "native", name)
	}
}

func TestNativeReplaceIntoAJSONDestinationChangesNothing(t *testing.T) {
	for _, extra := range [][]string{{"--replace", "--force"}, {"--dry-run"}, {"--replace", "--dry-run"}} {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			seedNativeSources(t)
			batches, cleared := useRecordingDestination(t, query.FormJSON)
			root, _ := newRootCmd()
			args := append([]string{"--native", "--src", "ddb", "--insert", "dst"}, extra...)
			_, err := runCmd(t, root, args...)
			require.ErrorContains(t, err, `--native reads form "dynamodb-json", but destination dst`)
			require.Zero(t, *cleared)
			require.Empty(t, *batches)
		})
	}
}

func TestMCPInsertNativeReplaceIntoAJSONDestinationChangesNothing(t *testing.T) {
	seedNativeSources(t)
	batches, cleared := useRecordingDestination(t, query.FormJSON)
	cs := connectMCP(t, newTestMCPServer([]string{allowWrites, allowDestructive}, 200, 256*1024), nil)
	body := errorBodyOf(t, callMCP(t, cs, "iq_insert", map[string]any{
		"source": "ddb", "destination": "dst", "native": true, "replace": true, "confirm": true,
	}))
	require.Contains(t, body.Error.Message, `--native reads form "dynamodb-json"`)
	require.Zero(t, *cleared)
	require.Empty(t, *batches)
}

func TestNativeRefusesASourceWithoutANativeReader(t *testing.T) {
	const want = "source dst (recput) has no native form. --native works with a DynamoDB source"
	tests := []struct {
		name string
		args []string
	}{
		{"query", []string{"--native", "--src", "dst", ".[]"}},
		{"typed dump", []string{"--native", "--typed", "--src", "dst"}},
		{"insert", []string{"--native", "--src", "dst", "--insert", "dst"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedNativeSources(t)
			batches, _ := useRecordingDestination(t, query.FormDynamoJSON)
			root, _ := newRootCmd()
			out, err := runCmd(t, root, tt.args...)
			require.EqualError(t, err, want)
			require.Empty(t, out)
			require.Empty(t, *batches)
		})
	}
}

func TestMCPQueryNativeRefusesASourceWithoutANativeReader(t *testing.T) {
	seedNativeSources(t)
	useRecordingDestination(t, query.FormDynamoJSON)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)
	body := errorBodyOf(t, callMCP(t, cs, "iq_query", map[string]any{
		"source": "dst", "filter": ".[]", "native": true,
	}))
	require.Equal(t, "source dst (recput) has no native form. --native works with a DynamoDB source", body.Error.Message)
}

func TestNativeRefusesADestinationThatCannotWriteTheForm(t *testing.T) {
	tests := []struct {
		name    string
		own     query.Form
		wantErr string
	}{
		{
			"json only", query.FormJSON,
			`--native reads form "dynamodb-json", but destination dst (recput) writes only form "json"`,
		},
		{
			"another native form", query.FormMongoEJSON,
			`--native reads form "dynamodb-json", but destination dst (recput) writes only forms "json" and "mongo-ejson"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedNativeSources(t)
			batches, cleared := useRecordingDestination(t, tt.own)
			root, _ := newRootCmd()
			_, err := runCmd(t, root, "--native", "--src", "ddb", "--insert", "dst")
			require.EqualError(t, err, tt.wantErr)
			require.Zero(t, *cleared)
			require.Empty(t, *batches)
		})
	}
}

func TestNativeExplainKeepsThePlanOfTheQuery(t *testing.T) {
	seedNativeSources(t)
	root, _ := newRootCmd()
	plain, err := runCmd(t, root, "--explain", "--src", "ddb", ".[]")
	require.NoError(t, err)
	root, _ = newRootCmd()
	native, err := runCmd(t, root, "--native", "--explain", "--src", "ddb", ".[]")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(native, plain), "native plan %q must start with %q", native, plain)
}

func TestNativeExplainReportsAPlanError(t *testing.T) {
	seedNativeSources(t)
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	boom := errors.New("plan boom")
	drivers = append(append([]driver{}, orig...), driver{
		name: "planfail", schemes: []string{"planfail"},
		explainPlan: func(planSource, selector.KeySet, predicate.Node, bool) (query.AccessPlan, error) {
			return query.AccessPlan{}, boom
		},
	})
	c := newSeed()
	require.NoError(t, c.Add("bad", "planfail://h"))
	seedConfig(t, c)
	root, _ := newRootCmd()
	out, err := runCmd(t, root, "--native", "--explain", "--src", "bad", ".[]")
	require.ErrorIs(t, err, boom)
	require.NotContains(t, out, "native read")
}

func TestTypedPutterStopsOnAWriteError(t *testing.T) {
	boom := errors.New("disk full")
	w := &failingWriter{err: boom}
	p := typedPutter{f: newFormatter(formatJSONL, w, true)}
	stat, err := p.Put(t.Context(), []query.Record{{Key: "a"}, {Key: "b"}}, query.Upsert)
	require.ErrorIs(t, err, boom)
	require.Zero(t, stat.Written)
	require.Equal(t, 1, w.writes)
}

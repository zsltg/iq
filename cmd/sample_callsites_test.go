package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// callsiteDump holds two items with distinct fields, so the sample cap and the
// filter each change the inferred field set.
var callsiteDump = []string{`{"key":"1","value":{"a":1}}`, `{"key":"2","value":{"b":2}}`}

// schemaProps decodes the top-level property names of a JSON Schema document.
func schemaProps(t *testing.T, doc string) []string {
	t.Helper()
	var s struct {
		Properties map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(doc), &s))
	names := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		names = append(names, k)
	}
	return names
}

// TestSchemaCmdPassesSampleAndRunOptions proves that `iq schema` hands its
// sample cap and the run options of its config to the sampler.
func TestSchemaCmdPassesSampleAndRunOptions(t *testing.T) {
	seedDiffFiles(t, map[string][]string{"src": callsiteDump})

	t.Run("the sample cap limits the items read", func(t *testing.T) {
		out, err := runCmd(t, newSchemaCmd(&config{timeout: 5 * time.Second}), "src", "--sample", "1")

		require.NoError(t, err)
		require.Len(t, schemaProps(t, out), 1)
	})

	t.Run("a sample of zero reads every item", func(t *testing.T) {
		out, err := runCmd(t, newSchemaCmd(&config{timeout: 5 * time.Second}), "src", "--sample", "0")

		require.NoError(t, err)
		require.ElementsMatch(t, []string{"a", "b"}, schemaProps(t, out))
	})

	t.Run("the config logger reaches the filtered read", func(t *testing.T) {
		var buf bytes.Buffer
		cfg := &config{timeout: 5 * time.Second, logger: captureRecords(&buf)}

		_, err := runCmd(t, newSchemaCmd(cfg), "src=.[]")

		require.NoError(t, err)
		require.Contains(t, buf.String(), "scan strategy")
	})
}

// TestMCPSchemaPassesFilterAndRunOptions proves that the iq_schema tool gives
// its filter and the run options of the server config to the sampler.
func TestMCPSchemaPassesFilterAndRunOptions(t *testing.T) {
	seedDiffFiles(t, map[string][]string{"src": callsiteDump})
	var buf bytes.Buffer
	s := newTestMCPServer(nil, 200, 256*1024)
	s.cfg.logger = captureRecords(&buf)
	cs := connectMCP(t, s, nil)

	t.Run("the filter scopes the inferred shape", func(t *testing.T) {
		var out map[string]any
		structOf(t, callMCP(t, cs, "iq_schema", map[string]any{"source": "src", "filter": ".[] | select(.a != null)"}), &out)

		props, _ := out["properties"].(map[string]any)
		require.Contains(t, props, "a")
		require.NotContains(t, props, "b")
	})

	t.Run("the server logger reaches the filtered read", func(t *testing.T) {
		buf.Reset()
		structOf(t, callMCP(t, cs, "iq_schema", map[string]any{"source": "src", "filter": ".[]"}), &map[string]any{})

		require.Contains(t, buf.String(), "scan strategy")
	})
}

// TestDiffSchemaPassesRunOptions proves that `diff --schema` samples a filtered
// source under the run options of its config.
func TestDiffSchemaPassesRunOptions(t *testing.T) {
	seedDiffFiles(t, map[string][]string{"a": callsiteDump, "b": callsiteDump})
	var buf bytes.Buffer
	cfg := &config{timeout: 5 * time.Second, logger: captureRecords(&buf)}

	_, err := runCmd(t, quietDiff(cfg), "a=.[]", "b=.[]", "--schema")

	require.NoError(t, err)
	require.Contains(t, buf.String(), "scan strategy")
}

// TestSampleItemsReturnsScanError proves that an unfiltered sample returns a
// scan error instead of the items read so far.
func TestSampleItemsReturnsScanError(t *testing.T) {
	boom := errors.New("scan broke")
	st := &fakeStore{pages: []map[string]any{{"k": 1}}, scanErr: boom}

	items, err := sampleItems(t.Context(), st, sampleRequest{})

	require.ErrorIs(t, err, boom)
	require.Nil(t, items)
}

// infoStore answers every query with a fixed reply, as a Redis INFO would.
type infoStore struct {
	*fakeStore
	reply string
}

func (s infoStore) Query(ctx context.Context, args []string) (any, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.reply, nil
}

// TestCollectRedisInfo pins the reply tree and the error path of the INFO read.
func TestCollectRedisInfo(t *testing.T) {
	spec := sourceSpec{handle: "r", url: "redis://h:6379/0"}

	t.Run("the reply becomes a section tree", func(t *testing.T) {
		st := infoStore{fakeStore: &fakeStore{}, reply: "# Server\r\nredis_version:7.0\r\n"}

		tree, err := collectRedisInfo(t.Context(), st, spec, nil)

		require.NoError(t, err)
		require.Equal(t, map[string]any{"Server": map[string]any{"redis_version": "7.0"}}, tree)
	})

	t.Run("a failed query returns the error", func(t *testing.T) {
		st := infoStore{fakeStore: &fakeStore{err: errors.New("down")}}

		tree, err := collectRedisInfo(t.Context(), st, spec, nil)

		require.ErrorContains(t, err, `inspect "r"`)
		require.Nil(t, tree)
	})
}

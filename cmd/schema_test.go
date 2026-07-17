package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeJSONL writes keyed-JSONL lines to a temp file and returns its path.
func writeJSONL(t *testing.T, lines string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dump.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(lines), 0o600))
	return path
}

// TestSchemaFileSource drives `iq schema` end-to-end against a connection-free
// file source and pins the emitted draft-07 document.
func TestSchemaFileSource(t *testing.T) {
	path := writeJSONL(t, `{"key":"1","value":{"name":"alice","age":30,"active":true}}
{"key":"2","value":{"name":"bob","age":25}}
`)
	c := newSeed()
	require.NoError(t, c.Add("snap", "file://"+path))
	seedConfig(t, c)

	cfg := &config{timeout: 5 * time.Second}
	out, err := runCmd(t, newSchemaCmd(cfg), "snap")
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	require.Equal(t, "http://json-schema.org/draft-07/schema#", doc["$schema"])
	require.Equal(t, "snap", doc["title"], "title is the handle, never the url")
	require.Equal(t, "object", doc["type"])

	props := doc["properties"].(map[string]any)
	require.Equal(t, "string", props["name"].(map[string]any)["type"])
	require.Equal(t, "integer", props["age"].(map[string]any)["type"])
	require.Equal(t, "boolean", props["active"].(map[string]any)["type"])
	// name and age are in every document; active is in one, so it is optional.
	require.Equal(t, []any{"age", "name"}, doc["required"])
}

// TestSchemaSampleCap pins the shared sampling helper's cap against a file store:
// a positive sample bounds the read, zero reads the whole keyspace.
func TestSchemaSampleCap(t *testing.T) {
	path := writeJSONL(t, `{"key":"1","value":{"a":1}}
{"key":"2","value":{"a":2}}
{"key":"3","value":{"a":3}}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := openStore(ctx, &config{url: "file://" + path})
	require.NoError(t, err)
	defer func() { _ = st.Close() }()

	tests := []struct {
		name   string
		sample int
		want   int
	}{
		{"cap of one reads one", 1, 1},
		{"cap of two reads two", 2, 2},
		{"cap above the keyspace reads all", 10, 3},
		{"zero reads the whole keyspace", 0, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, err := sampleItems(ctx, st, tt.sample)
			require.NoError(t, err)
			require.Len(t, items, tt.want)
		})
	}
}

func TestSchemaUnknownSource(t *testing.T) {
	seedConfig(t, newSeed())
	cfg := &config{timeout: 5 * time.Second}
	_, err := runCmd(t, newSchemaCmd(cfg), "nope")
	require.ErrorContains(t, err, "unknown source")
}

// TestSchemaCmdMetadata pins the command's help metadata — the Short line and the
// Example block — which the mutation gate clears field by field.
func TestSchemaCmdMetadata(t *testing.T) {
	c := newSchemaCmd(&config{})
	require.Equal(t, "Emit a draft-07 JSON Schema inferred from a sampled source", c.Short)
	require.Contains(t, c.Example, "iq schema")
	require.Contains(t, c.Example, "--sample")
}

// TestSchemaCmdSampleFlag pins the --sample flag: it is registered and defaults to
// 1000; the gate off-by-ones the default and removes the registration.
func TestSchemaCmdSampleFlag(t *testing.T) {
	c := newSchemaCmd(&config{})
	f := c.Flags().Lookup("sample")
	require.NotNil(t, f)
	require.Equal(t, "1000", f.DefValue)
}

// TestSchemaYAMLOutput drives `iq schema -y` and asserts a YAML document (not
// JSON), pinning the --yaml flag registration and its -y shorthand.
func TestSchemaYAMLOutput(t *testing.T) {
	path := writeJSONL(t, `{"key":"1","value":{"name":"alice"}}
`)
	c := newSeed()
	require.NoError(t, c.Add("snap", "file://"+path))
	seedConfig(t, c)

	cfg := &config{timeout: 5 * time.Second}
	out, err := runCmd(t, newSchemaCmd(cfg), "snap", "-y")
	require.NoError(t, err)
	require.Contains(t, out, "$schema:")
	require.NotContains(t, out, `"$schema"`)
}

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

func TestParseRedisInfo(t *testing.T) {
	info := "# Server\r\nredis_version:7.2.0\r\nos:Linux\r\n\r\n# Memory\r\nused_memory:12345\r\n"
	got := parseRedisInfo(info)
	require.Equal(t, "7.2.0", got["Server"]["redis_version"])
	require.Equal(t, "Linux", got["Server"]["os"])
	require.Equal(t, "12345", got["Memory"]["used_memory"])
}

func TestMongoInspectDoc(t *testing.T) {
	require.Equal(t, `{"dbStats":1}`, mongoInspectDoc("dbStats", ""))
	require.Equal(t, `{"collStats":"books"}`, mongoInspectDoc("collStats", "books"))
}

func TestIsMongoInspectCmd(t *testing.T) {
	require.True(t, isMongoInspectCmd("dbStats"))
	require.True(t, isMongoInspectCmd("collStats"))
	require.False(t, isMongoInspectCmd("dropDatabase"))
}

func TestInspectMongoRejectsUnknown(t *testing.T) {
	// Validation happens before any store access, so a nil store is never used.
	var buf bytes.Buffer
	err := inspectMongo(context.Background(), &buf, nil, &config{url: "mongodb://h/db"}, []string{"dropDatabase"}, false, false)
	require.ErrorContains(t, err, "unknown inspect subcommand")
}

func TestInspectMongoCollStatsNeedsCollection(t *testing.T) {
	var buf bytes.Buffer
	err := inspectMongo(context.Background(), &buf, nil, &config{url: "mongodb://h/db"}, []string{"collStats"}, false, false)
	require.ErrorContains(t, err, "needs a collection")
}

func TestInspectMongoList(t *testing.T) {
	// The list path prints the supported set and never touches the store, so a
	// nil store proves it stays offline.
	var buf bytes.Buffer
	err := inspectMongo(context.Background(), &buf, nil, &config{url: "mongodb://h/db"}, nil, false, true)
	require.NoError(t, err)
	for _, sub := range mongoInspectCmds {
		require.Contains(t, buf.String(), sub)
	}
}

func TestInspectHeader(t *testing.T) {
	fk := useFakeKeyring(t)
	require.NoError(t, fk.Set("sec", "secret"))

	inline := iqconfig.Source{URL: "redis://u:secret@h:6379/0"}
	keyring := iqconfig.Source{URL: "redis://u@h:6379/0", Keyring: true}

	tests := []struct {
		name         string
		source       iqconfig.Source
		handle       string
		reveal       bool
		expand       bool
		wantContains string
		wantAbsent   string
	}{
		{"inline redacted by default", inline, "cache", false, false, "redis://u:xxxxx@h:6379/0", "secret"},
		{"inline reveal un-redacts", inline, "cache", true, false, "redis://u:secret@h:6379/0", "xxxxx"},
		{"keyring hidden by default", keyring, "sec", false, false, "redis://u@h:6379/0", "secret"},
		{"keyring reveal alone stays hidden", keyring, "sec", true, false, "redis://u@h:6379/0", "secret"},
		{"keyring expand alone redacts", keyring, "sec", false, true, "redis://u:xxxxx@h:6379/0", "secret"},
		{"keyring reveal and expand", keyring, "sec", true, true, "redis://u:secret@h:6379/0", "xxxxx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cfg := &config{source: tt.source, handle: tt.handle, reveal: tt.reveal, expand: tt.expand}
			require.NoError(t, inspectHeader(&buf, cfg))
			require.Contains(t, buf.String(), "redis  ") // driver from the source scheme
			require.Contains(t, buf.String(), tt.wantContains)
			require.NotContains(t, buf.String(), tt.wantAbsent)
		})
	}
}

func TestRedisInfoSections(t *testing.T) {
	info := "# Server\r\nredis_version:7.2.0\r\n\r\n# Memory\r\nused_memory:12345\r\n"
	require.Equal(t, []string{"Memory", "Server"}, redisInfoSections(info))
}

func TestWriteInspectList(t *testing.T) {
	t.Run("plain is newline separated", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, writeInspectList(&buf, []string{"a", "b"}, false))
		require.Equal(t, "a\nb\n", buf.String())
	})
	t.Run("json is an array", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, writeInspectList(&buf, []string{"a", "b"}, true))
		var got []string
		require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		require.Equal(t, []string{"a", "b"}, got)
	})
}

func TestInspectRedisIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable Redis")
	}
	url := os.Getenv("IQ_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	c := newSeed()
	require.NoError(t, c.Add("live", url, ""))
	require.NoError(t, c.SetActive("live"))
	seedConfig(t, c)

	cfg := &config{timeout: 3 * time.Second}

	out, err := runCmd(t, newInspectCmd(cfg), "server")
	require.NoError(t, err)
	require.Contains(t, out, "redis_version")

	jsonOut, err := runCmd(t, newInspectCmd(cfg), "server", "--json")
	require.NoError(t, err)
	var sections map[string]map[string]string
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &sections))
	require.Contains(t, sections, "Server")
	require.NotEmpty(t, sections["Server"]["redis_version"])

	listOut, err := runCmd(t, newInspectCmd(cfg), "--list")
	require.NoError(t, err)
	require.Contains(t, listOut, "Server")
	require.Contains(t, listOut, "Memory")

	listJSON, err := runCmd(t, newInspectCmd(cfg), "--list", "--json")
	require.NoError(t, err)
	var names []string
	require.NoError(t, json.Unmarshal([]byte(listJSON), &names))
	require.Contains(t, names, "Server")
}

func TestInspectMongoIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable MongoDB")
	}
	url := os.Getenv("IQ_MONGO_URL")
	if url == "" {
		url = "mongodb://localhost:27017/iq"
	}
	c := newSeed()
	require.NoError(t, c.Add("live", url, "books"))
	require.NoError(t, c.SetActive("live"))
	seedConfig(t, c)

	cfg := &config{timeout: 5 * time.Second}

	out, err := runCmd(t, newInspectCmd(cfg), "dbStats")
	require.NoError(t, err)
	require.Contains(t, out, "dbStats")

	// No args runs every subcommand; the text output must show them all, not stop
	// after the first.
	allText, err := runCmd(t, newInspectCmd(cfg))
	require.NoError(t, err)
	require.Contains(t, allText, "# dbStats")
	require.Contains(t, allText, "# buildInfo")

	all, err := runCmd(t, newInspectCmd(cfg), "--json")
	require.NoError(t, err)
	var byName map[string]any
	require.NoError(t, json.Unmarshal([]byte(all), &byName))
	require.Contains(t, byName, "dbStats")
	require.Contains(t, byName, "listCollections")

	// --list runs through RunE (opening the store) and prints the supported set.
	listOut, err := runCmd(t, newInspectCmd(cfg), "--list")
	require.NoError(t, err)
	require.Contains(t, listOut, "dbStats")
	require.Contains(t, listOut, "buildInfo")
}

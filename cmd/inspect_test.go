package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	err := inspectMongo(context.Background(), &buf, nil, &config{url: "mongodb://h/db"}, []string{"dropDatabase"}, false)
	require.ErrorContains(t, err, "unknown inspect subcommand")
}

func TestInspectMongoCollStatsNeedsCollection(t *testing.T) {
	var buf bytes.Buffer
	err := inspectMongo(context.Background(), &buf, nil, &config{url: "mongodb://h/db"}, []string{"collStats"}, false)
	require.ErrorContains(t, err, "needs a collection")
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
}

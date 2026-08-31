package redis_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	_ "github.com/zsltg/iq/drivers/redis"
)

// loggerProbeEnv marks the child process this test re-executes; see
// TestPackageSilencesGoRedisLogger.
const loggerProbeEnv = "IQ_TEST_REDIS_LOGGER_PROBE"

// TestPackageSilencesGoRedisLogger pins the package init's SetLogger: go-redis
// prints its internal warnings to stderr through a package global, which would
// interleave raw library noise into iq's own output.
//
// That global has no getter, so the only way to observe it is to make go-redis
// want to log: a sub-millisecond expiration makes it warn while the command is
// still being built, before any I/O, so a queued pipeline command is enough and no
// server is involved. Its default logger captured os.Stderr when the library's own
// init ran, so reassigning os.Stderr here would not catch the write; the probe runs
// in a child copy of this test binary instead, whose stderr this process owns.
func TestPackageSilencesGoRedisLogger(t *testing.T) {
	if os.Getenv(loggerProbeEnv) == "1" {
		// Child: queue a command whose expiration go-redis warns about. The pipeline
		// is never executed, so nothing is dialed and only the warning is at stake.
		p := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"}).Pipeline()
		p.Set(context.Background(), "iq:test:logger-probe", "v", time.Nanosecond)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPackageSilencesGoRedisLogger$", "-test.short")
	cmd.Env = append(os.Environ(), loggerProbeEnv+"=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	require.NoError(t, cmd.Run(), "probe process failed: %s", stderr.String())
	require.Empty(t, stderr.String(), "go-redis's global logger must be silenced by the package init")
}

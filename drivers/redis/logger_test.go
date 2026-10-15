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

// loggerProbeEnv marks the child process that this test
// re-executes. See
// TestPackageSilencesGoRedisLogger.
const loggerProbeEnv = "IQ_TEST_REDIS_LOGGER_PROBE"

// TestPackageSilencesGoRedisLogger pins the SetLogger call in the package
// init. go-redis prints its internal warnings to stderr through a package
// global. Without the SetLogger call, that global mixes raw library noise into
// the output of iq.
//
// That global has no getter, so the only way to observe it is to make go-redis
// want to log. A sub-millisecond expiration makes it warn while the command is
// still being built, before any I/O. So a queued pipeline command is enough and
// no server is involved. Its default logger captured os.Stderr when the init of
// the library ran. So reassigning os.Stderr here does not catch the write.
// Instead, the probe runs in a child copy of this test binary, whose stderr this
// process owns.
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

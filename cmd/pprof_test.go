package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStartProfileOff(t *testing.T) {
	stop, err := startProfile("", io.Discard)
	require.NoError(t, err)
	require.Nil(t, stop)
}

// TestStartProfileInvalid rejects an unknown mode before creating any file.
func TestStartProfileInvalid(t *testing.T) {
	t.Chdir(t.TempDir())
	stop, err := startProfile("cpuu", io.Discard)
	require.Error(t, err)
	require.Nil(t, stop)

	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	require.Empty(t, entries, "an invalid mode must not create a file")
}

// TestStartProfileWritesFile runs each mode end to end: it starts, the stop func
// finalizes a non-empty file with the expected name, and reports the path.
func TestStartProfileWritesFile(t *testing.T) {
	tests := []struct {
		mode string
		file string
	}{
		{"cpu", "cpu.pprof"},
		{"mem", "mem.pprof"},
		{"block", "block.pprof"},
		{"mutex", "mutex.pprof"},
		{"goroutine", "goroutine.pprof"},
		{"thread", "thread.pprof"},
		{"trace", "trace.out"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Chdir(t.TempDir())
			var msg bytes.Buffer

			stop, err := startProfile(tt.mode, &msg)
			require.NoError(t, err)
			require.NotNil(t, stop)
			stop()

			fi, err := os.Stat(tt.file)
			require.NoError(t, err)
			require.Positive(t, fi.Size(), "profile file must not be empty")

			abs, err := filepath.Abs(tt.file)
			require.NoError(t, err)
			require.Contains(t, msg.String(), abs)
		})
	}
}

func TestProfileFilename(t *testing.T) {
	require.Equal(t, "cpu.pprof", profileFilename("cpu"))
	require.Equal(t, "trace.out", profileFilename("trace"))
}

func TestLookupName(t *testing.T) {
	require.Equal(t, "heap", lookupName("mem"))
	require.Equal(t, "threadcreate", lookupName("thread"))
	require.Equal(t, "block", lookupName("block"))
}

// TestStartProfileFailures covers a profile file that cannot be created and a
// profiler that is already running.
func TestStartProfileFailures(t *testing.T) {
	t.Run("an unwritable directory", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("chmod does not stop writes on Windows, so this failure cannot be forced there")
		}
		for _, mode := range []string{"cpu", "trace", "mem", "mutex"} {
			t.Run(mode, func(t *testing.T) {
				dir := t.TempDir()
				require.NoError(t, os.Chmod(dir, 0o500))
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
				t.Chdir(dir)
				stop, err := startProfile(mode, io.Discard)
				require.ErrorContains(t, err, "create profile file")
				require.Nil(t, stop)
				t.Cleanup(func() { runtime.SetMutexProfileFraction(0) })
			})
		}
	})

	for _, mode := range []string{"cpu", "trace"} {
		t.Run("a second "+mode+" profile is refused", func(t *testing.T) {
			t.Chdir(t.TempDir())
			stop, err := startProfile(mode, io.Discard)
			require.NoError(t, err)
			t.Cleanup(stop)

			second, err := startProfile(mode, io.Discard)
			require.Error(t, err)
			require.Nil(t, second)
		})
	}
}

// TestStartProfileSamplingSettings checks the mutex profile is armed at full
// rate and the heap profile is written in the binary format `go tool pprof` reads.
func TestStartProfileSamplingSettings(t *testing.T) {
	t.Run("mutex is armed at every event", func(t *testing.T) {
		t.Chdir(t.TempDir())
		t.Cleanup(func() { runtime.SetMutexProfileFraction(0) })
		stop, err := startProfile("mutex", io.Discard)
		require.NoError(t, err)
		require.Equal(t, 1, runtime.SetMutexProfileFraction(-1))
		stop()
	})

	t.Run("mem writes the compressed binary format", func(t *testing.T) {
		t.Chdir(t.TempDir())
		stop, err := startProfile("mem", io.Discard)
		require.NoError(t, err)
		stop()
		raw, err := os.ReadFile("mem.pprof")
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(raw), 2)
		require.Equal(t, []byte{0x1f, 0x8b}, raw[:2], "gzip magic of a debug=0 profile")
	})
}

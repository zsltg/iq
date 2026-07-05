package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
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

package cmd

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestSelectFormatParquet(t *testing.T) {
	got, err := selectFormat(&config{format: "parquet"})
	require.NoError(t, err)
	require.Equal(t, formatParquet, got)
}

func TestValidateFormatParquet(t *testing.T) {
	require.NoError(t, validateFormat("parquet"))
	err := validateFormat("csv")
	require.Error(t, err)
	require.Contains(t, err.Error(), "parquet", "the hint should list parquet as a valid format")
}

func TestGuardBinaryFormat(t *testing.T) {
	tests := []struct {
		name    string
		format  outputFormat
		tty     bool
		wantErr bool
	}{
		{name: "parquet to a terminal is refused", format: formatParquet, tty: true, wantErr: true},
		{name: "parquet to a pipe is allowed", format: formatParquet, tty: false},
		{name: "json to a terminal is allowed", format: formatJSON, tty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := binaryTTYCheck
			binaryTTYCheck = func(io.Writer) bool { return tt.tty }
			t.Cleanup(func() { binaryTTYCheck = orig })

			err := guardBinaryFormat(tt.format, io.Discard)
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "terminal")
				require.Contains(t, err.Error(), "redirect")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestSelectTypedFormatRejectsParquet(t *testing.T) {
	_, err := selectTypedFormat(&config{format: "parquet"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "parquet")
}

// TestParquetFormatterWiring drives the formatter as the query path does — emit
// per value, then flush — and asserts a real Parquet file lands in the sink (the
// PAR1 magic bookends the format).
func TestParquetFormatterWiring(t *testing.T) {
	var buf bytes.Buffer
	f := newFormatter(formatParquet, &buf, false)
	require.NoError(t, f.emit(map[string]any{"n": 1}))
	require.NoError(t, f.emit(map[string]any{"n": 2}))
	require.NoError(t, f.flush())

	out := buf.Bytes()
	require.Greater(t, len(out), 8)
	require.Equal(t, "PAR1", string(out[:4]), "file should open with the Parquet magic")
	require.Equal(t, "PAR1", string(out[len(out)-4:]), "file should close with the Parquet magic")
}

// TestParquetFormatterPropagatesEncodeError drives the formatter as the query
// path does and forces a post-sample type mismatch, so emit must surface the
// encoder's error rather than swallowing it.
func TestParquetFormatterPropagatesEncodeError(t *testing.T) {
	f := newFormatter(formatParquet, &bytes.Buffer{}, false)
	// The parquetout sample is 1000 values; fill it with an int column, then a
	// string in that column past the sample fails.
	for range 1000 {
		require.NoError(t, f.emit(map[string]any{"n": 1}))
	}
	err := f.emit(map[string]any{"n": "not-an-int"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "encode result")
}

// TestRunJQRefusesParquetToTerminal exercises the guard inside runJQ: with the
// terminal check forced true, the run must refuse before any store I/O and write
// nothing.
func TestRunJQRefusesParquetToTerminal(t *testing.T) {
	origTTY := binaryTTYCheck
	binaryTTYCheck = func(io.Writer) bool { return true }
	t.Cleanup(func() { binaryTTYCheck = origTTY })
	origStdin := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = origStdin })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetIn(strings.NewReader(""))

	err := runJQ(cmd, &config{format: "parquet", timeout: time.Second}, ".")
	require.Error(t, err)
	require.Contains(t, err.Error(), "terminal")
	require.Empty(t, buf.Bytes(), "nothing should be written when the format is refused")
}

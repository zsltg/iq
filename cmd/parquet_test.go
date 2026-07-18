package cmd

import (
	"bytes"
	"io"
	"testing"

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

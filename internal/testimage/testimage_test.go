package testimage

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRef(t *testing.T) {
	const valid = `services:
  redis:
    image: redis:8.10.2@sha256:abc
  bare:
    container_name: iq-bare
`
	tests := []struct {
		name    string
		content *string
		service string
		want    string
		wantErr string
		// wrapped is true when the error must keep its cause for errors.Is and errors.As.
		wrapped bool
	}{
		{name: "known service", content: new(valid), service: "redis", want: "redis:8.10.2@sha256:abc"},
		{name: "unknown service", content: new(valid), service: "mongo", wantErr: `service "mongo" is not in`},
		{name: "service without image", content: new(valid), service: "bare", wantErr: `service "bare" in`},
		{name: "invalid yaml", content: new("services: [unclosed"), service: "redis", wantErr: "testimage: parse", wrapped: true},
		{name: "missing file", content: nil, service: "redis", wantErr: "testimage: read", wrapped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "compose.yaml")
			if tt.content != nil {
				require.NoError(t, os.WriteFile(path, []byte(*tt.content), 0o600))
			}

			got, err := ref(path, tt.service)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Empty(t, got)
				require.Equal(t, tt.wrapped, errors.Unwrap(err) != nil, "error keeps its cause")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestRefMissingFileIsNotExist(t *testing.T) {
	_, err := ref(filepath.Join(t.TempDir(), "compose.yaml"), "redis")

	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestRefReadsRepositoryCompose(t *testing.T) {
	got, err := Ref("redis")

	require.NoError(t, err)
	require.Regexp(t, `^redis:[0-9]+\.[0-9]+\.[0-9]+@sha256:[0-9a-f]{64}$`, got)
}

func TestRefRejectsEmptyService(t *testing.T) {
	_, err := Ref("")

	require.EqualError(t, err, "testimage: empty service name")
}

func TestRefWithoutModuleRoot(t *testing.T) {
	t.Chdir(t.TempDir())

	_, err := Ref("redis")

	require.EqualError(t, err, "testimage: no go.mod above the working directory")
}

func TestRefWorkingDirectoryGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gone")
	require.NoError(t, os.Mkdir(dir, 0o700))
	t.Chdir(dir)
	require.NoError(t, os.Remove(dir))

	_, err := Ref("redis")

	require.ErrorContains(t, err, "testimage: get working directory")
	require.Error(t, errors.Unwrap(err), "error keeps its cause")
}

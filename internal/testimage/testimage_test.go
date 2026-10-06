package testimage

import (
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
	}{
		{name: "known service", content: new(valid), service: "redis", want: "redis:8.10.2@sha256:abc"},
		{name: "unknown service", content: new(valid), service: "mongo", wantErr: `service "mongo" is not in`},
		{name: "service without image", content: new(valid), service: "bare", wantErr: `service "bare"`},
		{name: "invalid yaml", content: new("services: [unclosed"), service: "redis", wantErr: "parse"},
		{name: "missing file", content: nil, service: "redis", wantErr: "read"},
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
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestRefReadsRepositoryCompose(t *testing.T) {
	got, err := Ref("redis")

	require.NoError(t, err)
	require.Regexp(t, `^redis:[0-9]+\.[0-9]+\.[0-9]+@sha256:[0-9a-f]{64}$`, got)
}

func TestRefRejectsEmptyService(t *testing.T) {
	_, err := Ref("")

	require.Error(t, err)
}

package cmd

import (
	"bytes"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name     string
		injected string
		info     *debug.BuildInfo
		ok       bool
		want     string
	}{
		{
			name:     "injected tag wins",
			injected: "v1.2.3",
			info:     &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}},
			ok:       true,
			want:     "v1.2.3",
		},
		{
			name:     "no build info falls back to dev",
			injected: "dev",
			ok:       false,
			want:     "dev",
		},
		{
			name:     "module version used when injected is dev",
			injected: "dev",
			info:     &debug.BuildInfo{Main: debug.Module{Version: "v0.4.0"}},
			ok:       true,
			want:     "v0.4.0",
		},
		{
			name:     "devel module version ignored, vcs revision used",
			injected: "dev",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abcdef1234567890"}},
			},
			ok:   true,
			want: "dev+abcdef1234567890",
		},
		{
			name:     "empty module version, no vcs revision, stays dev",
			injected: "dev",
			info:     &debug.BuildInfo{Main: debug.Module{Version: ""}},
			ok:       true,
			want:     "dev",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, resolveVersion(tt.injected, tt.info, tt.ok))
		})
	}
}

func TestVcsRevision(t *testing.T) {
	tests := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{
			name: "revision returned verbatim, skipping other settings",
			info: &debug.BuildInfo{Settings: []debug.BuildSetting{
				{Key: "vcs.time", Value: "2026-07-04T00:00:00Z"},
				{Key: "vcs.revision", Value: "0123456789abcdef"},
			}},
			want: "0123456789abcdef",
		},
		{
			name: "revision absent yields empty",
			info: &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, vcsRevision(tt.info))
		})
	}
}

func TestVersionCmd(t *testing.T) {
	cmd := newVersionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})

	require.NoError(t, cmd.Execute())

	got := out.String()
	require.Contains(t, got, "iq "+buildVersion())
	require.Contains(t, got, "commit: "+commit)
	require.Contains(t, got, "built:  "+date)
	require.Contains(t, got, "go:     "+runtime.Version())
}

func TestRootCommandHasVersion(t *testing.T) {
	require.NotEmpty(t, newRootCmd().Version)
}

package redis

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseURLHidesPassword(t *testing.T) {
	const hint = "percent-encode special characters in the password"
	tests := []struct {
		name     string
		uri      string
		wantHint bool
	}{
		{"slash in password", "redis://u:/dummysecret@host:6379/0", true},
		{"question mark in password", "redis://u:?dummysecret@host:6379/0", true},
		{"slash after digits", "redis://u:123/dummysecret@host", true},
		{"bad port without at sign", "redis://host:bad/0", false},
		{"bad database without at sign", "redis://host:6379/x", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseURL(tc.uri)
			require.ErrorIs(t, err, ErrInvalidURI)
			require.NotContains(t, err.Error(), "dummysecret")
			if tc.wantHint {
				require.Contains(t, err.Error(), hint)
			} else {
				require.NotContains(t, err.Error(), hint)
			}
		})
	}
}

func TestParseURLValid(t *testing.T) {
	opts, err := parseURL("redis://u:p@host:6379/0")
	require.NoError(t, err)
	require.Equal(t, "host:6379", opts.Addr)
}

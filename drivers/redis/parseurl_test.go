package redis

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseURLHidesPassword(t *testing.T) {
	tests := []struct {
		name string
		uri  string
	}{
		{"slash in password", "redis://u:/dummysecret@host:6379/0"},
		{"question mark in password", "redis://u:?dummysecret@host:6379/0"},
		{"slash after digits", "redis://u:123/dummysecret@host"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseURL(tc.uri)
			require.ErrorIs(t, err, ErrInvalidURI)
			require.NotContains(t, err.Error(), "dummysecret")
		})
	}
}

func TestParseURLValid(t *testing.T) {
	opts, err := parseURL("redis://u:p@host:6379/0")
	require.NoError(t, err)
	require.Equal(t, "host:6379", opts.Addr)
}

package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSchemeOf(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"redis", "redis://localhost:6379/0", "redis"},
		{"redis tls", "rediss://host:6379", "rediss"},
		{"mongodb", "mongodb://localhost:27017/iq", "mongodb"},
		{"mongodb srv", "mongodb+srv://host/iq", "mongodb+srv"},
		{"uppercase normalized", "REDIS://host", "redis"},
		{"multi-host mongo", "mongodb://h1,h2:27017/iq", "mongodb"},
		{"no scheme", "localhost:6379", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, schemeOf(tt.url))
		})
	}
}

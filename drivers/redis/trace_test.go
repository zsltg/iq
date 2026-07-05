package redis

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatRedisCmd(t *testing.T) {
	tests := []struct {
		name string
		args []any
		want string
	}{
		{name: "empty", args: nil, want: ""},
		{name: "simple get", args: []any{"get", "book:1"}, want: "GET book:1"},
		{name: "type", args: []any{"TYPE", "user:1"}, want: "TYPE user:1"},
		{name: "scan", args: []any{"scan", 0, "match", "*", "count", 100}, want: "SCAN 0 match * count 100"},
		{name: "auth fully masked", args: []any{"AUTH", "s3cr3t"}, want: "AUTH (redacted)"},
		{name: "auth with user masked", args: []any{"auth", "alice", "s3cr3t"}, want: "AUTH (redacted)"},
		{
			name: "hello auth credentials masked",
			args: []any{"HELLO", "3", "AUTH", "alice", "s3cr3t"},
			want: "HELLO 3 AUTH (redacted) (redacted)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, formatRedisCmd(tt.args))
		})
	}
}

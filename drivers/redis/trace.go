package redis

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	goredis "github.com/redis/go-redis/v9"
)

// traceHook logs every Redis command to a writer, backing the CLI's --verbose
// command trace. Writes are serialized so commands issued from a pipeline never
// tear each other's lines. Credentials are never logged (see formatRedisCmd).
type traceHook struct {
	mu sync.Mutex
	w  io.Writer
}

// newTraceHook returns a hook that writes each command to w.
func newTraceHook(w io.Writer) *traceHook { return &traceHook{w: w} }

// DialHook passes through: connection dialing is not part of the query trace.
func (h *traceHook) DialHook(next goredis.DialHook) goredis.DialHook { return next }

// ProcessHook logs a single command before it runs.
func (h *traceHook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error {
		h.log(cmd)
		return next(ctx, cmd)
	}
}

// ProcessPipelineHook logs each command of a pipeline before the batch runs.
func (h *traceHook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []goredis.Cmder) error {
		for _, cmd := range cmds {
			h.log(cmd)
		}
		return next(ctx, cmds)
	}
}

// log writes one command line under the lock.
func (h *traceHook) log(cmd goredis.Cmder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, _ = fmt.Fprintf(h.w, "redis> %s\n", formatRedisCmd(cmd.Args()))
}

// formatRedisCmd renders a command's args as an uppercased command name followed
// by its arguments, redacting any AUTH credential so no password reaches the
// trace: a bare AUTH command is fully masked, and the credentials after an AUTH
// token in a HELLO handshake are masked.
func formatRedisCmd(args []any) string {
	if len(args) == 0 {
		return ""
	}
	name := strings.ToUpper(fmt.Sprint(args[0]))
	if name == "AUTH" {
		return "AUTH (redacted)"
	}
	parts := make([]string, 0, len(args))
	parts = append(parts, name)
	redactRest := false
	for _, a := range args[1:] {
		if redactRest {
			parts = append(parts, "(redacted)")
			continue
		}
		s := fmt.Sprint(a)
		parts = append(parts, s)
		if name == "HELLO" && strings.EqualFold(s, "AUTH") {
			redactRest = true
		}
	}
	return strings.Join(parts, " ")
}

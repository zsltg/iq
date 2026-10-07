package redis

import (
	"context"
	"fmt"
	"sync"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/zsltg/iq/internal/numfmt"
)

// fakeKey is one key of the in-memory keyspace behind fakeRedis. typ is the
// Redis TYPE the fake reports. val is the reply of the type's value command: a
// string, a map[string]string, a []string, a []goredis.Z, a []goredis.XMessage,
// or the JSON text of a RedisJSON document. err, when set, is the error of the
// value command, as a key that vanished mid-read would give.
type fakeKey struct {
	typ string
	val any
	err error
}

// fakeCall is one call that reached the fake: the context it carried and every
// command with its arguments, in order.
type fakeCall struct {
	ctx   context.Context //nolint:containedctx // The test asserts which context reached the server.
	piped bool
	cmds  [][]any
}

// fakeRedis is a go-redis hook that answers commands from memory and never
// dials. It records the context and the arguments of every call, so a test
// asserts them exactly. A test sets keys, scan rounds, integer replies and
// failures before it runs the code under test.
type fakeRedis struct {
	mu sync.Mutex
	// keys is the keyspace that TYPE and the value commands read.
	keys map[string]fakeKey
	// scan holds the keys that each SCAN round returns. Round i returns scan[i]
	// and the next cursor, which is 0 after the last round.
	scan [][]string
	// ints maps "name key" (for example "del a") to the reply of that command.
	ints map[string]int64
	// fail maps the index of a call to the transport error that call returns.
	fail map[int]error

	calls []fakeCall
}

// newFakeStore returns a Store whose client talks to a fake. The page size and
// the decimal mode are the same as an opened Store would have, except that the
// test sets pageSize.
func newFakeStore(t *testing.T, f *fakeRedis, pageSize int) *Store {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	client.AddHook(f)
	return &Store{client: client, pageSize: pageSize, decimal: numfmt.DecimalAuto}
}

// snapshot returns a copy of the recorded calls.
func (f *fakeRedis) snapshot() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

// DialHook passes through. The fake answers before any dial.
func (f *fakeRedis) DialHook(next goredis.DialHook) goredis.DialHook { return next }

// ProcessHook answers one command.
func (f *fakeRedis) ProcessHook(goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error {
		return f.answer(ctx, false, []goredis.Cmder{cmd})
	}
}

// ProcessPipelineHook answers one pipeline.
func (f *fakeRedis) ProcessPipelineHook(goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []goredis.Cmder) error {
		return f.answer(ctx, true, cmds)
	}
}

// answer records the call, then either fails it or fills in each reply. Like the
// real client, it returns the first command error.
func (f *fakeRedis) answer(ctx context.Context, piped bool, cmds []goredis.Cmder) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := fakeCall{ctx: ctx, piped: piped}
	for _, c := range cmds {
		call.cmds = append(call.cmds, append([]any(nil), c.Args()...))
	}
	index := len(f.calls)
	f.calls = append(f.calls, call)
	if err := f.fail[index]; err != nil {
		return err
	}
	var first error
	for _, c := range cmds {
		f.reply(c)
		if first == nil {
			first = c.Err()
		}
	}
	return first
}

// reply fills in the reply of one command.
func (f *fakeRedis) reply(c goredis.Cmder) {
	args := c.Args()
	name := fmt.Sprint(args[0])
	key := ""
	if len(args) > 1 {
		key = fmt.Sprint(args[1])
	}
	k, found := f.keys[key]
	switch cmd := c.(type) {
	case *goredis.StatusCmd:
		if !found {
			k.typ = "none"
		}
		cmd.SetVal(k.typ)
	case *goredis.StringCmd:
		if k.err != nil {
			cmd.SetErr(k.err)
			return
		}
		cmd.SetVal(fmt.Sprint(k.val))
	case *goredis.MapStringStringCmd:
		cmd.SetVal(replyAs[map[string]string](k.val))
		cmd.SetErr(k.err)
	case *goredis.StringSliceCmd:
		cmd.SetVal(replyAs[[]string](k.val))
		cmd.SetErr(k.err)
	case *goredis.ZSliceCmd:
		cmd.SetVal(replyAs[[]goredis.Z](k.val))
		cmd.SetErr(k.err)
	case *goredis.XMessageSliceCmd:
		cmd.SetVal(replyAs[[]goredis.XMessage](k.val))
		cmd.SetErr(k.err)
	case *goredis.JSONCmd:
		if k.err != nil {
			cmd.SetErr(k.err)
			return
		}
		cmd.SetVal(fmt.Sprint(k.val))
	case *goredis.ScanCmd:
		f.replyScan(cmd, args)
	case *goredis.IntCmd:
		cmd.SetVal(f.ints[name+" "+key])
	}
}

// replyScan answers one SCAN round from f.scan.
func (f *fakeRedis) replyScan(cmd *goredis.ScanCmd, args []any) {
	var round int
	_, _ = fmt.Sscan(fmt.Sprint(args[1]), &round)
	if round >= len(f.scan) {
		cmd.SetVal(nil, 0)
		return
	}
	next := uint64(round + 1)
	if round+1 >= len(f.scan) {
		next = 0
	}
	cmd.SetVal(f.scan[round], next)
}

// replyAs converts a fake value to the reply type, or returns the zero value.
func replyAs[T any](v any) T {
	t, _ := v.(T)
	return t
}

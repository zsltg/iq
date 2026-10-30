package redis

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/zsltg/iq/internal/numfmt"
)

// fakeKey is one key of the in-memory keyspace behind fakeRedis. The field typ
// is the Redis TYPE that the fake reports. The field val is the reply of the
// value command of the type. It is JSON text for a RedisJSON document, or
// one of these Go values: a string, a map[string]string, a []string, a
// []goredis.Z, or a []goredis.XMessage. The field err, when set, is the error
// of the value command, as a key that vanished mid-read gives. The field per,
// when above zero, makes HSCAN, SSCAN and ZSCAN return that many elements per
// round. The field repeat makes each later round return the last element of the
// round before it again, as a rehashing table can. The scan commands return
// elements in reverse order, so a test sees that the reader sorts.
type fakeKey struct {
	typ    string
	val    any
	err    error
	per    int
	repeat bool
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
// the decimal mode are the same as those of an opened Store, except that the
// test sets pageSize.
func newFakeStore(t *testing.T, f *fakeRedis, pageSize int) *Store {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	client.AddHook(f)
	return &Store{client: client, pageSize: pageSize, decimal: numfmt.DecimalAuto, limits: defaultLimits()}
}

// newPipeline returns a pipeline that only queues commands. Its client never
// dials the address, so a test reads the queued commands and nothing runs.
func newPipeline(t *testing.T) goredis.Pipeliner {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	return client.Pipeline()
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
	case *goredis.StringSliceCmd:
		replyRange(cmd, args, k)
	case *goredis.XMessageSliceCmd:
		replyStream(cmd, args, k)
	case *goredis.JSONCmd:
		if k.err != nil {
			cmd.SetErr(k.err)
			return
		}
		cmd.SetVal(fmt.Sprint(k.val))
	case *goredis.ScanCmd:
		if name == "scan" {
			f.replyScan(cmd, args)
			return
		}
		replyElements(cmd, args, k)
	case *goredis.IntCmd:
		n, set := f.ints[name+" "+key]
		if !set {
			n = int64(sizeOf(k.val))
		}
		cmd.SetVal(n)
	}
}

// sizeOf returns the element count that HLEN, LLEN, SCARD, ZCARD and XLEN give.
func sizeOf(val any) int {
	switch v := val.(type) {
	case map[string]string:
		return len(v)
	case []string:
		return len(v)
	case []goredis.Z:
		return len(v)
	case []goredis.XMessage:
		return len(v)
	}
	return 0
}

// replyRange answers LRANGE from a []string value. Other string-slice commands
// get the whole value.
func replyRange(cmd *goredis.StringSliceCmd, args []any, k fakeKey) {
	vs := replyAs[[]string](k.val)
	if fmt.Sprint(args[0]) == "lrange" {
		var start, stop int
		_, _ = fmt.Sscan(fmt.Sprint(args[2]), &start)
		_, _ = fmt.Sscan(fmt.Sprint(args[3]), &stop)
		if stop < 0 || stop >= len(vs) {
			stop = len(vs) - 1
		}
		if start > stop {
			vs = nil
		} else {
			vs = vs[start : stop+1]
		}
	}
	cmd.SetVal(vs)
	cmd.SetErr(k.err)
}

// replyStream answers XRANGE with the optional COUNT, from a []goredis.XMessage.
func replyStream(cmd *goredis.XMessageSliceCmd, args []any, k fakeKey) {
	start := fmt.Sprint(args[2])
	var out []goredis.XMessage
	for _, m := range replyAs[[]goredis.XMessage](k.val) {
		if start == "-" || streamIDLess(start, m.ID) || start == m.ID {
			out = append(out, m)
		}
	}
	if len(args) > 5 {
		var n int
		_, _ = fmt.Sscan(fmt.Sprint(args[5]), &n)
		out = out[:min(n, len(out))]
	}
	cmd.SetVal(out)
	cmd.SetErr(k.err)
}

// streamIDLess compares two IDs of the form "<ms>-<seq>" that have one digit group each.
func streamIDLess(a, b string) bool {
	var ams, aseq, bms, bseq uint64
	_, _ = fmt.Sscanf(a, "%d-%d", &ams, &aseq)
	_, _ = fmt.Sscanf(b, "%d-%d", &bms, &bseq)
	return ams < bms || (ams == bms && aseq < bseq)
}

// replyElements answers HSCAN, SSCAN and ZSCAN from the value of the key. The
// cursor of a round is the index of its first element, counted in elements.
func replyElements(cmd *goredis.ScanCmd, args []any, k fakeKey) {
	flat := flatten(k.val)
	var from uint64
	_, _ = fmt.Sscan(fmt.Sprint(args[2]), &from)
	to := uint64(len(flat))
	step := uint64(max(k.per, 1)) * uint64(width(k.val))
	if k.per > 0 {
		to = min(to, from+step)
	}
	round := append([]string(nil), flat[from:to]...)
	if k.repeat && from > 0 {
		round = append(round, flat[from-uint64(width(k.val)):from]...)
	}
	next := to
	if to >= uint64(len(flat)) {
		next = 0
	}
	cmd.SetVal(round, next)
	cmd.SetErr(k.err)
}

// width is how many reply items one element of the value takes: two for a hash or
// a sorted set, one for a set.
func width(val any) int {
	if _, ok := val.([]string); ok {
		return 1
	}
	return 2
}

// flatten returns the elements of a hash, set or sorted set as the flat reply of
// a scan command, in reverse of the stored order.
func flatten(val any) []string {
	var flat []string
	switch v := val.(type) {
	case map[string]string:
		for _, name := range slices.Backward(slices.Sorted(maps.Keys(v))) {
			flat = append(flat, name, v[name])
		}
	case []string:
		for _, m := range slices.Backward(v) {
			flat = append(flat, m)
		}
	case []goredis.Z:
		for _, z := range slices.Backward(v) {
			flat = append(flat, z.Member.(string), strconv.FormatFloat(z.Score, 'g', -1, 64))
		}
	}
	return flat
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

package cmd

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// mvLog records what the fake move stores see, in order.
type mvLog struct {
	mu       sync.Mutex
	events   []string
	ctxs     map[string]context.Context
	modes    []query.WriteMode
	batches  [][]query.Record
	clearErr error
	putErr   error
	closed   int
}

func (l *mvLog) event(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *mvLog) seen(ctx context.Context, where string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ctxs[where] = ctx
}

// mvBare is a store with no write and no typed read port.
type mvBare struct {
	fakeInspectStore
	log *mvLog
}

func (s mvBare) Close() error {
	s.log.mu.Lock()
	defer s.log.mu.Unlock()
	s.log.closed++
	return nil
}

// mvPut is a store that can be written but not cleared.
type mvPut struct {
	mvBare
	url string
}

func (s mvPut) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	s.log.event("put")
	s.log.seen(ctx, "put")
	s.log.mu.Lock()
	defer s.log.mu.Unlock()
	s.log.modes = append(s.log.modes, mode)
	s.log.batches = append(s.log.batches, batch)
	if s.log.putErr != nil {
		return query.WriteStat{}, fmt.Errorf("write to %s: %w", s.url, s.log.putErr)
	}
	return query.WriteStat{Written: len(batch)}, nil
}

// mvFull is a store that can be read, written and cleared.
type mvFull struct{ mvPut }

func (s mvFull) Clear(ctx context.Context) error {
	s.log.event("clear")
	s.log.seen(ctx, "clear")
	if s.log.clearErr != nil {
		return fmt.Errorf("clear %s: %w", s.url, s.log.clearErr)
	}
	return nil
}

func (s mvFull) TypedScan(ctx context.Context, fn func([]query.Record) error) error {
	s.log.seen(ctx, "scan")
	return fn([]query.Record{{Key: "k", Type: "string", Value: "v"}})
}

// useMoveDrivers registers the mvfull, mvput and mvbare schemes and seeds one
// source of each under the handles full, put and bare. The full source URI
// carries a password so that a redaction check has something to hide.
func useMoveDrivers(t *testing.T) *mvLog {
	t.Helper()
	log := &mvLog{ctxs: map[string]context.Context{}}
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	mk := func(scheme string, build func(url string) store) driver {
		return driver{
			name: scheme, schemes: []string{scheme},
			open: func(ctx context.Context, cfg *config) (store, error) {
				log.seen(ctx, "open:"+scheme)
				return build(cfg.url), nil
			},
		}
	}
	drivers = append(append([]driver{}, orig...),
		mk("mvfull", func(u string) store { return mvFull{mvPut{mvBare{log: log}, u}} }),
		mk("mvput", func(u string) store { return mvPut{mvBare{log: log}, u} }),
		mk("mvbare", func(string) store { return mvBare{log: log} }),
		driver{
			name: "mvfail", schemes: []string{"mvfail"},
			open: func(_ context.Context, cfg *config) (store, error) {
				return nil, fmt.Errorf("dial %s: refused", cfg.url)
			},
		},
	)
	c := newSeed()
	require.NoError(t, c.Add("full", "mvfull://u:hunter2@h"))
	require.NoError(t, c.Add("put", "mvput://h"))
	require.NoError(t, c.Add("bare", "mvbare://h"))
	require.NoError(t, c.Add("down", "mvfail://u:hunter2@h"))
	seedConfig(t, c)
	return log
}

// oneBatch is a record source that offers one batch of two records.
func oneBatch(_ context.Context, fn func([]query.Record) error) error {
	return fn([]query.Record{{Key: "a", Type: "string", Value: "1"}, {Key: "b", Type: "string", Value: "2"}})
}

func TestApplyInsertOrder(t *testing.T) {
	tests := []struct {
		name string
		prep func(t *testing.T)
		req  insertRequest
		want string
	}{
		{"a corrupt config", func(t *testing.T) { corruptConfig(t, "") }, insertRequest{dst: "full"}, "parse config"},
		{"an unknown destination", nil, insertRequest{dst: "nosuch"}, "nosuch"},
		{"a saved dump file", func(t *testing.T) { seedFileSource(t, moveDump) }, insertRequest{dst: "snap"}, "destination snap (file) cannot be written to"},
		{"a store with no Put", nil, insertRequest{dst: "bare"}, "destination bare (mvbare) cannot be written to"},
		{"replace on a store with no Clear", nil, insertRequest{dst: "put", replace: true}, "--replace: destination put (mvput) cannot be cleared"},
		{"replace on a dry run still needs Clear", nil, insertRequest{dst: "put", replace: true, dryRun: true}, "--replace: destination put (mvput) cannot be cleared"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := useMoveDrivers(t)
			if tt.prep != nil {
				tt.prep(t)
			}
			tt.req.confirm = func(string) error { t.Fatal("confirm must not run"); return nil }
			_, _, err := applyInsert(context.Background(), tt.req, oneBatch, nil)
			require.ErrorContains(t, err, tt.want)
			require.Empty(t, log.events, "nothing is cleared or written")
		})
	}
}

func TestApplyInsertReplaceFlow(t *testing.T) {
	run := func(t *testing.T, log *mvLog, req insertRequest) (string, query.WriteStat, error) {
		t.Helper()
		req.dst = "full"
		req.replace = true
		if req.confirm == nil {
			req.confirm = func(action string) error { log.event("confirm:" + action); return nil }
		}
		return applyInsert(context.Background(), req, oneBatch, nil)
	}

	t.Run("confirm, then clear, then put", func(t *testing.T) {
		log := useMoveDrivers(t)
		label, stat, err := run(t, log, insertRequest{mode: query.Upsert})
		require.NoError(t, err)
		require.Equal(t, "full", label)
		require.Equal(t, 2, stat.Written)
		require.Equal(t, []string{"confirm:clear full before writing", "clear", "put"}, log.events)
		require.Equal(t, 1, log.closed, "the store is closed once")
	})

	t.Run("a confirm error stops before Clear and Put", func(t *testing.T) {
		log := useMoveDrivers(t)
		refuse := errors.New("no thanks")
		_, _, err := run(t, log, insertRequest{confirm: func(string) error { return refuse }})
		require.Same(t, refuse, err)
		require.Empty(t, log.events)
		require.Equal(t, 1, log.closed)
	})

	t.Run("a Clear error is redacted", func(t *testing.T) {
		log := useMoveDrivers(t)
		log.clearErr = errors.New("boom")
		_, _, err := run(t, log, insertRequest{})
		require.ErrorContains(t, err, "mvfull://u:xxxxx@h")
		require.NotContains(t, err.Error(), "hunter2")
		require.NotContains(t, log.events, "put")
	})

	t.Run("a Put error is redacted", func(t *testing.T) {
		log := useMoveDrivers(t)
		log.putErr = errors.New("boom")
		_, _, err := run(t, log, insertRequest{})
		require.ErrorContains(t, err, "mvfull://u:xxxxx@h")
		require.NotContains(t, err.Error(), "hunter2")
	})

	t.Run("a dry run neither asks, clears nor writes", func(t *testing.T) {
		log := useMoveDrivers(t)
		_, stat, err := run(t, log, insertRequest{dryRun: true})
		require.NoError(t, err)
		require.Empty(t, log.events)
		require.Equal(t, 2, stat.Written, "the plan still counts the records")
	})
}

type mvCtxKey struct{}

func TestApplyInsertReachesTheStore(t *testing.T) {
	for _, mode := range []query.WriteMode{query.InsertOnly, query.Upsert} {
		t.Run(fmt.Sprint("mode ", mode), func(t *testing.T) {
			log := useMoveDrivers(t)
			ctx := context.WithValue(context.Background(), mvCtxKey{}, "marker")
			upper := func(r query.Record) ([]query.Record, error) {
				r.Value = "T" + r.Value.(string)
				return []query.Record{r}, nil
			}
			req := insertRequest{dst: "full", mode: mode, replace: true, confirm: func(string) error { return nil }}
			_, _, err := applyInsert(ctx, req, oneBatch, upper)
			require.NoError(t, err)
			require.Equal(t, []query.WriteMode{mode}, log.modes)
			require.Equal(t, "T1", log.batches[0][0].Value)
			for _, where := range []string{"open:mvfull", "clear", "put"} {
				require.Equal(t, "marker", log.ctxs[where].Value(mvCtxKey{}), where)
			}
		})
	}
}

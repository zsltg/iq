package cmd

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// lifeStore is a store with the destructive capabilities. It records the context
// each call received, so a test can prove the caller's context reached the
// backend, and what was asked of it.
type lifeStore struct {
	*fakeStore
	cleared, dropped bool
	deletedKeys      []string
	ctxs             []context.Context
}

func (s *lifeStore) Clear(ctx context.Context) error {
	s.ctxs = append(s.ctxs, ctx)
	s.cleared = true
	return nil
}

func (s *lifeStore) Drop(ctx context.Context) error {
	s.ctxs = append(s.ctxs, ctx)
	s.dropped = true
	return nil
}

func (s *lifeStore) Delete(ctx context.Context, keys []string) (query.DeleteStat, error) {
	s.ctxs = append(s.ctxs, ctx)
	s.deletedKeys = keys
	return query.DeleteStat{Deleted: len(keys) - 1, Missing: 1}, nil
}

func (s *lifeStore) EstimateCount(ctx context.Context) (int64, error) {
	s.ctxs = append(s.ctxs, ctx)
	return 7, nil
}

// useLifeDriver registers the life:// driver with a lifeStore and the plain
// bare:// driver whose store has no destructive capability, and seeds one source
// on each, plus a source whose backend is down.
func useLifeDriver(t *testing.T) *lifeStore {
	t.Helper()
	st := &lifeStore{fakeStore: &fakeStore{}}
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	drivers = append(append([]driver{}, orig...),
		driver{
			name:    "life",
			schemes: []string{"life"},
			open: func(ctx context.Context, _ *config) (store, error) {
				st.ctxs = append(st.ctxs, ctx)
				return st, nil
			},
			explainClear: func() query.AccessPlan { return query.AccessPlan{Ops: []string{"CLEAR life"}} },
			explainDrop: func() (query.AccessPlan, bool) {
				return query.AccessPlan{Ops: []string{"DROP life"}}, true
			},
			explainDelete: func() (query.AccessPlan, bool) {
				return query.AccessPlan{Ops: []string{"DELETE life"}}, true
			},
		},
		driver{
			name:    "broken",
			schemes: []string{"broken"},
			open: func(context.Context, *config) (store, error) {
				return nil, errors.New("backend down")
			},
		},
		driver{
			name:    "bare",
			schemes: []string{"bare"},
			open: func(context.Context, *config) (store, error) {
				return &fakeStore{}, nil
			},
		},
	)
	c := newSeed()
	require.NoError(t, c.Add("a", "life://a"))
	require.NoError(t, c.Add("bare", "bare://b"))
	require.NoError(t, c.Add("broken", "broken://b"))
	seedConfig(t, c)
	return st
}

// runLife runs one data lifecycle command and returns what it printed to stdout
// and to stderr.
func runLife(t *testing.T, build func(*config, *dataFlags) *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cfg := &config{timeout: 5 * time.Second}
	df := &dataFlags{}
	c := build(cfg, df)
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = orig })
	c.Flags().BoolVar(&df.explain, "explain", false, "")
	c.Flags().BoolVar(&df.dryRun, "dry-run", false, "")
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetArgs(args)
	err = c.ExecuteContext(markedCtx())
	return out.String(), errb.String(), err
}

// TestDataLifecycleCommands runs clear, drop and delete against a fake backend
// and checks the effect, the report line and the context the backend received.
func TestDataLifecycleCommands(t *testing.T) {
	tests := []struct {
		name     string
		build    func(*config, *dataFlags) *cobra.Command
		args     []string
		wantLine string
		check    func(t *testing.T, st *lifeStore)
	}{
		{
			name:     "clear",
			build:    newDataClearCmd,
			args:     []string{"a", "--force"},
			wantLine: "cleared a",
			check:    func(t *testing.T, st *lifeStore) { require.True(t, st.cleared) },
		},
		{
			name:     "drop",
			build:    newDataDropCmd,
			args:     []string{"a", "--force"},
			// The command misspells "dropped" today. A follow-up fix(cmd)
			// change corrects the output and updates this row with it.
			wantLine: "droped a",
			check:    func(t *testing.T, st *lifeStore) { require.True(t, st.dropped) },
		},
		{
			name:     "clear dry run reports the estimate",
			build:    newDataClearCmd,
			args:     []string{"a", "--dry-run"},
			wantLine: "would clear a (~7 item(s))",
			check:    func(t *testing.T, st *lifeStore) { require.False(t, st.cleared) },
		},
		{
			name:     "delete dedupes the keys",
			build:    newDataDeleteCmd,
			args:     []string{"a", "k1", "k2", "k1"},
			wantLine: "deleted 1 key(s), 1 already absent",
			check:    func(t *testing.T, st *lifeStore) { require.Equal(t, []string{"k1", "k2"}, st.deletedKeys) },
		},
		{
			name:     "delete dry run",
			build:    newDataDeleteCmd,
			args:     []string{"a", "k1", "--dry-run"},
			wantLine: "would delete 1 key(s) from a",
			check:    func(t *testing.T, st *lifeStore) { require.Nil(t, st.deletedKeys) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := useLifeDriver(t)
			_, stderr, err := runLife(t, tt.build, tt.args...)
			require.NoError(t, err)
			require.Contains(t, stderr, tt.wantLine)
			tt.check(t, st)
			require.True(t, st.closed, "the store is closed after the command")
			require.NotEmpty(t, st.ctxs, "the backend was opened")
			for _, ctx := range st.ctxs {
				require.NotNil(t, ctx.Value(ctxKey{}), "the backend must get the caller's context")
			}
		})
	}
}

// TestDataLifecycleRefusals covers what the commands refuse.
func TestDataLifecycleRefusals(t *testing.T) {
	tests := []struct {
		name    string
		build   func(*config, *dataFlags) *cobra.Command
		args    []string
		wantErr string
	}{
		{name: "clear without a target", build: newDataClearCmd, wantErr: "arg(s)"},
		{name: "drop without a target", build: newDataDropCmd, wantErr: "arg(s)"},
		{name: "delete without keys", build: newDataDeleteCmd, args: []string{"a"}, wantErr: "arg(s)"},
		{name: "clear a backend without the capability", build: newDataClearCmd, args: []string{"bare", "--force"}, wantErr: "does not support clear"},
		{name: "drop a backend without the capability", build: newDataDropCmd, args: []string{"bare", "--force"}, wantErr: "does not support drop"},
		{name: "delete on a backend without the capability", build: newDataDeleteCmd, args: []string{"bare", "k"}, wantErr: "does not support delete"},
		{name: "clear a file", build: newDataClearCmd, args: []string{"-", "--force"}, wantErr: "needs a source, not a file"},
		{name: "delete from a file", build: newDataDeleteCmd, args: []string{"-", "k"}, wantErr: "needs a source, not a file"},
		{name: "delete with an empty source", build: newDataDeleteCmd, args: []string{"", "k"}, wantErr: "a source is required"},
		{name: "delete from an unknown source", build: newDataDeleteCmd, args: []string{"nosuch", "k"}, wantErr: "nosuch"},
		{name: "delete with an empty key", build: newDataDeleteCmd, args: []string{"a", ""}, wantErr: "empty key"},
		{name: "clear on a down backend", build: newDataClearCmd, args: []string{"broken", "--force"}, wantErr: "backend down"},
		{name: "delete on a down backend", build: newDataDeleteCmd, args: []string{"broken", "k"}, wantErr: "backend down"},
		{name: "clear needs confirmation", build: newDataClearCmd, args: []string{"a"}, wantErr: "needs confirmation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := useLifeDriver(t)
			_, _, err := runLife(t, tt.build, tt.args...)
			require.ErrorContains(t, err, tt.wantErr)
			require.False(t, st.cleared || st.dropped)
			require.Nil(t, st.deletedKeys)
		})
	}
}

// TestDataLifecyclePlan checks the --explain output: the heading, the target, the
// driver section and the describer's operations.
func TestDataLifecyclePlan(t *testing.T) {
	tests := []struct {
		name  string
		build func(*config, *dataFlags) *cobra.Command
		args  []string
		want  []string
	}{
		{name: "clear", build: newDataClearCmd, args: []string{"a", "--explain"}, want: []string{"clear plan", "a", "life clear", "  CLEAR life"}},
		{name: "drop", build: newDataDropCmd, args: []string{"a", "--explain"}, want: []string{"drop plan", "life drop", "  DROP life"}},
		{name: "delete", build: newDataDeleteCmd, args: []string{"a", "k", "--explain"}, want: []string{"delete plan", "life delete", "  DELETE life"}},
		{name: "driver without a describer", build: newDataClearCmd, args: []string{"bare", "--explain"}, want: []string{"bare clear"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := useLifeDriver(t)
			stdout, _, err := runLife(t, tt.build, tt.args...)
			require.NoError(t, err)
			for _, w := range tt.want {
				require.Contains(t, stdout, w)
			}
			require.False(t, st.cleared || st.dropped, "--explain never connects")
		})
	}
}

// TestLifecycleDescribe checks each op's describer against drivers with and
// without the explain hook.
func TestLifecycleDescribe(t *testing.T) {
	plan := query.AccessPlan{Ops: []string{"X"}}
	withAll := driver{
		explainClear:  func() query.AccessPlan { return plan },
		explainDrop:   func() (query.AccessPlan, bool) { return plan, true },
		explainDelete: func() (query.AccessPlan, bool) { return plan, true },
	}
	for _, op := range []lifecycleOp{clearOp, dropOp, deleteOp} {
		t.Run(op.name, func(t *testing.T) {
			got, ok := op.describe(withAll)
			require.True(t, ok)
			require.Equal(t, plan, got)

			got, ok = op.describe(driver{})
			require.False(t, ok)
			require.Empty(t, got.Ops)
		})
	}
}

package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// TestUnboundedFlagReachesTheDriverContext pins that --unbounded changes the
// context of a read. A driver uses this context to allow one very large value.
func TestUnboundedFlagReachesTheDriverContext(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"a key read without the flag", []string{"--src", "a", `.["k1"]`}, false},
		{"a key read with the flag", []string{"--src", "a", `.["k1"]`, "--unbounded"}, true},
		{"a scan with the flag", []string{"--src", "a", ".[]", "--unbounded"}, true},
		{"a scan without the flag", []string{"--src", "a", ".[]"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStore{pages: []map[string]any{{"k1": 1.0}}}
			useRecordingDriver(t, st)
			root, _ := newRootCmd()

			_, err := runCmd(t, root, tt.args...)

			require.NoError(t, err)
			require.NotNil(t, st.gotCtx)
			require.Equal(t, tt.want, query.Unbounded(st.gotCtx))
		})
	}
}

// useRecordingDriver registers the rec:// driver, whose store is st, and saves a
// source named "a" that points to it.
func useRecordingDriver(t *testing.T, st *fakeStore) {
	t.Helper()
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	drivers = append(append([]driver{}, orig...), driver{
		name:        "rec",
		schemes:     []string{"rec"},
		addressable: true,
		open:        func(context.Context, *config) (store, error) { return st, nil },
		explainPlan: func(planSource, selector.KeySet, predicate.Node, bool) (query.AccessPlan, error) {
			return query.AccessPlan{}, nil
		},
	})
	c := newSeed()
	require.NoError(t, c.Add("a", "rec://a"))
	seedConfig(t, c)
}

// TestUnboundedFlagReachesTheContextOfDiffAndSchema pins the flag on the two
// read commands that do not run a jq engine over the whole read.
func TestUnboundedFlagReachesTheContextOfDiffAndSchema(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"diff data without the flag", []string{"diff", "a", "a"}, false},
		{"diff data with the flag", []string{"diff", "a", "a", "--unbounded"}, true},
		{"diff data with a filter and the flag", []string{"diff", "a=.[]", "a=.[]", "--unbounded"}, true},
		{"diff schema with the flag", []string{"diff", "a", "a", "--schema", "--unbounded"}, true},
		{"diff schema without the flag", []string{"diff", "a", "a", "--schema"}, false},
		{"schema with the flag", []string{"schema", "a", "--unbounded"}, true},
		{"schema without the flag", []string{"schema", "a"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStore{pages: []map[string]any{{"k1": 1.0}}}
			useRecordingDriver(t, st)
			root, _ := newRootCmd()

			_, err := runCmd(t, root, tt.args...)
			if err != nil {
				require.ErrorIs(t, err, errQuietExit)
			}
			require.NotNil(t, st.gotCtx)
			require.Equal(t, tt.want, query.Unbounded(st.gotCtx))
		})
	}
}

// TestUnboundedFlagReachesTheContextOfAMove pins the flag on the typed read of a
// copy or a move.
func TestUnboundedFlagReachesTheContextOfAMove(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"insert without the flag", []string{"--src", "full", "--insert", "other"}, false},
		{"insert with the flag", []string{"--src", "full", "--insert", "other", "--unbounded"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := useMoveDrivers(t)
			root, _ := newRootCmd()

			_, err := runCmd(t, root, tt.args...)

			require.NoError(t, err)
			require.Equal(t, tt.want, query.Unbounded(log.ctxs["scan"]))
		})
	}
}

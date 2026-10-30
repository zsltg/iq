package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// unboundedProbeStore records whether the context of a statement is unbounded.
type unboundedProbeStore struct {
	fakeInspectStore
	unbounded *bool
}

func (s unboundedProbeStore) Query(ctx context.Context, _ []string) (any, error) {
	*s.unbounded = query.Unbounded(ctx)
	return []any{}, nil
}

// TestCLIExecIsUnbounded pins that the CLI exec runs its statement unbounded, so a
// capped read such as an HBase row comes back whole.
func TestCLIExecIsUnbounded(t *testing.T) {
	unbounded := false
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	drivers = append(append([]driver{}, orig...), driver{
		name: "unbfake", schemes: []string{"unbfake"},
		open: func(context.Context, *config) (store, error) {
			return unboundedProbeStore{unbounded: &unbounded}, nil
		},
	})
	c := newSeed()
	require.NoError(t, c.Add("probe", "unbfake://h"))
	seedConfig(t, c)

	_, _, err := runSplit(t, "exec", "--src", "probe", "scan", "t")

	require.NoError(t, err)
	require.True(t, unbounded)
}

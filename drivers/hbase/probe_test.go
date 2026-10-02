package hbase

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestOpenStopsAtTheCallersDeadline(t *testing.T) {
	// Nothing listens on port 1, so ClusterStatus would retry without end. The probe
	// has to give up at the caller's deadline instead.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	start := time.Now()
	_, err := Open(ctx, "hbase://127.0.0.1:1/", "", nil, numfmt.DecimalAuto)
	require.ErrorContains(t, err, "connect hbase")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestProbe(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name    string
		admin   func(block chan struct{}) *fakeAdmin
		ctx     func(t *testing.T) context.Context
		wantErr error
		within  time.Duration
	}{
		{
			name:  "the cluster answers",
			admin: func(chan struct{}) *fakeAdmin { return &fakeAdmin{} },
			ctx:   func(*testing.T) context.Context { return context.Background() },
		},
		{
			name:    "the cluster fails",
			admin:   func(chan struct{}) *fakeAdmin { return &fakeAdmin{clusterErr: errBackend} },
			ctx:     func(*testing.T) context.Context { return context.Background() },
			wantErr: errBackend,
		},
		{
			name:    "the context ended before the call and the cluster answers",
			admin:   func(chan struct{}) *fakeAdmin { return &fakeAdmin{} },
			ctx:     func(*testing.T) context.Context { return cancelled },
			wantErr: context.Canceled,
		},
		{
			name:  "the cluster never answers",
			admin: func(block chan struct{}) *fakeAdmin { return &fakeAdmin{clusterBlock: block} },
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				t.Cleanup(cancel)
				return ctx
			},
			wantErr: context.DeadlineExceeded,
			within:  time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := make(chan struct{})
			t.Cleanup(func() { close(block) })
			st := newFakeStore(newFakeClient(), tt.admin(block), "", typeMap{}, ctAuto)
			ctx := tt.ctx(t)

			start := time.Now()
			err := st.probe(ctx)

			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}
			if tt.within > 0 {
				require.Less(t, time.Since(start), tt.within)
			}
		})
	}
}

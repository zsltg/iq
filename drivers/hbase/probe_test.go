package hbase

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tsuna/gohbase"
	"github.com/tsuna/gohbase/hrpc"
	"github.com/tsuna/gohbase/pb"
	"google.golang.org/protobuf/proto"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestOpenStopsAtTheCallersDeadline(t *testing.T) {
	// Nothing listens on port 1, so ClusterStatus would retry without end. The probe
	// has to give up at the caller's deadline instead.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	before := runtime.NumGoroutine()
	start := time.Now()
	_, err := Open(ctx, "hbase://127.0.0.1:1/", "", nil, numfmt.DecimalAuto)
	require.ErrorContains(t, err, "connect hbase")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
	// The cancelled request must not leave its retry loop behind.
	require.Eventually(t, func() bool { return runtime.NumGoroutine() <= before+2 }, 5*time.Second, 50*time.Millisecond)
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

func TestGohbaseAdminClientSendsRPCs(t *testing.T) {
	// Without SendRPC the probe falls back to a call that nothing can stop. This test
	// fails when a gohbase upgrade drops the method.
	admin := gohbase.NewAdminClient("127.0.0.1:1")
	_, ok := admin.(rpcSender)
	require.True(t, ok)
}

func TestProbeFallbackEndedContextWinsOverResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	admin := &fakeAdmin{clusterCancel: cancel}
	st := newFakeStore(newFakeClient(), admin, "", typeMap{}, ctAuto)
	require.ErrorIs(t, st.probe(ctx), context.Canceled)
}

func TestProbeRPC(t *testing.T) {
	errSend := errors.New("send failed")
	ok := func(hrpc.Call) (proto.Message, error) { return &pb.GetClusterStatusResponse{}, nil }
	tests := []struct {
		name    string
		send    func(cancel func()) func(hrpc.Call) (proto.Message, error)
		wantErr error
		wantMsg string
	}{
		{
			name: "the cluster answers",
			send: func(func()) func(hrpc.Call) (proto.Message, error) { return ok },
		},
		{
			name: "the request carries the caller's context",
			send: func(cancel func()) func(hrpc.Call) (proto.Message, error) {
				return func(c hrpc.Call) (proto.Message, error) {
					cancel()
					require.ErrorIs(t, c.Context().Err(), context.Canceled)
					return nil, errSend
				}
			},
			wantErr: context.Canceled,
		},
		{
			name: "the send fails",
			send: func(func()) func(hrpc.Call) (proto.Message, error) {
				return func(hrpc.Call) (proto.Message, error) { return nil, errSend }
			},
			wantErr: errSend,
		},
		{
			name: "the response has the wrong type",
			send: func(func()) func(hrpc.Call) (proto.Message, error) {
				return func(hrpc.Call) (proto.Message, error) { return &pb.GetTableNamesResponse{}, nil }
			},
			wantMsg: "unexpected response type",
		},
		{
			name: "the context ended and the cluster answers",
			send: func(cancel func()) func(hrpc.Call) (proto.Message, error) {
				return func(c hrpc.Call) (proto.Message, error) {
					cancel()
					return &pb.GetClusterStatusResponse{}, nil
				}
			},
			wantErr: context.Canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			admin := &fakeRPCAdmin{send: tt.send(cancel)}
			st := newFakeStore(newFakeClient(), admin, "", typeMap{}, ctAuto)

			err := st.probe(ctx)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantMsg != "":
				require.ErrorContains(t, err, tt.wantMsg)
			default:
				require.NoError(t, err)
			}
		})
	}
}

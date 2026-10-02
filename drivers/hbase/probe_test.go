package hbase

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
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
		noCall  bool
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
			noCall:  true,
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
			admin := tt.admin(block)
			st := newFakeStore(newFakeClient(), admin, "", typeMap{}, ctAuto)
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
			if tt.noCall {
				// The call runs in a goroutine, so watch for a late start.
				require.Never(t, func() bool { return admin.clusterCalls.Load() > 0 }, 50*time.Millisecond, time.Millisecond)
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

// lateEndContext reports no error until the select in probeCall reads Done. Done waits
// until the fake has answered, so the answer and the ended context are both ready when
// the select runs.
type lateEndContext struct {
	context.Context
	admin *fakeAdmin
	ended atomic.Bool
}

func (c *lateEndContext) Done() <-chan struct{} {
	for c.admin.clusterCalls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(5 * time.Millisecond)
	c.ended.Store(true)
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (c *lateEndContext) Err() error {
	if c.ended.Load() {
		return context.Canceled
	}
	return nil
}

func TestProbeFallbackEndedContextWinsOverResult(t *testing.T) {
	// With the answer and the ended context both ready, select picks one at random.
	// Repeat so that a missing check fails with near certainty.
	for range 50 {
		admin := &fakeAdmin{}
		st := newFakeStore(newFakeClient(), admin, "", typeMap{}, ctAuto)

		err := st.probe(&lateEndContext{Context: context.Background(), admin: admin})

		require.ErrorIs(t, err, context.Canceled)
	}
}

func TestProbeFallbackGoroutineEndsAfterTheContext(t *testing.T) {
	block := make(chan struct{})
	admin := &fakeAdmin{clusterBlock: block}
	st := newFakeStore(newFakeClient(), admin, "", typeMap{}, ctAuto)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	t.Cleanup(cancel)

	require.ErrorIs(t, st.probe(ctx), context.DeadlineExceeded)
	// The call goroutine is blocked in the fake now. Check that the frame name is
	// right, so that a rename cannot make the wait below pass with nothing to wait for.
	require.True(t, probeGoroutineRuns(), "no goroutine runs %s while the fake blocks", probeFrame)
	close(block)

	// The call has to send its result without a reader, or its goroutine stays. Poll
	// on this goroutine, because Eventually runs its condition on another one.
	deadline := time.Now().Add(5 * time.Second)
	for probeGoroutineRuns() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.False(t, probeGoroutineRuns(), "the goroutine of %s did not end after the fake returned", probeFrame)
}

// probeFrame is the function of the goroutine that probeCall starts.
const probeFrame = "hbase.(*Store).probeCall.func1"

// probeGoroutineRuns reports whether any goroutine stack holds probeFrame.
func probeGoroutineRuns() bool {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Contains(string(buf[:n]), probeFrame)
		}
		buf = make([]byte, 2*len(buf))
	}
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

func TestProbeRPCDoesNotSendWithAnEndedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	admin := &fakeRPCAdmin{send: func(hrpc.Call) (proto.Message, error) { return &pb.GetClusterStatusResponse{}, nil }}
	st := newFakeStore(newFakeClient(), admin, "", typeMap{}, ctAuto)

	err := st.probe(ctx)

	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, admin.sendCalls.Load())
}

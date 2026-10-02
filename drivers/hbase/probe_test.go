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
	require.Less(t, time.Since(start), 15*time.Second)
}

func TestProbe(t *testing.T) {
	t.Run("the cluster answers", func(t *testing.T) {
		st := newFakeStore(newFakeClient(), &fakeAdmin{}, "", typeMap{}, ctAuto)
		require.NoError(t, st.probe(context.Background()))
	})
	t.Run("the cluster fails", func(t *testing.T) {
		st := newFakeStore(newFakeClient(), &fakeAdmin{clusterErr: errBackend}, "", typeMap{}, ctAuto)
		require.ErrorIs(t, st.probe(context.Background()), errBackend)
	})
	t.Run("the cluster never answers", func(t *testing.T) {
		block := make(chan struct{})
		t.Cleanup(func() { close(block) })
		st := newFakeStore(newFakeClient(), &fakeAdmin{clusterBlock: block}, "", typeMap{}, ctAuto)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		t.Cleanup(cancel)
		require.ErrorIs(t, st.probe(ctx), context.DeadlineExceeded)
	})
}

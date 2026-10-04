package couchbase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/couchbase/gocb/v2"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// The tests in this file open a source that no server answers. They need no cluster.

// tightCtx narrows a cluster test context to 15 seconds. These tests make one call that
// takes well under a second, so a longer wait means a broken driver.
func tightCtx(t *testing.T, parent context.Context) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// boundedCtx returns a context that ends after a few seconds and ends with the test. A
// mutant that lets a call wait for a server must fail the test fast, not block it.
func boundedCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestConnectTimeoutIsLiteral pins the readiness bound at 15 seconds. A test that waits
// for the bound would add 15 seconds to every run of the suite, so the test reads the
// constant.
func TestConnectTimeoutIsLiteral(t *testing.T) {
	require.Equal(t, 15*time.Second, connectTimeout)
}

// TestNoBucketStore pins the guard that each keyspace operation runs first. A store with
// no collection refuses the call before it touches the cluster, so no server is needed.
func TestNoBucketStore(t *testing.T) {
	ctx := boundedCtx(t)
	st := &Store{}
	tests := []struct {
		name string
		op   func() error
	}{
		{name: "get", op: func() error { _, err := st.Get(ctx, []string{"1"}); return err }},
		{name: "scan", op: func() error { return st.ScanBatches(ctx, func(map[string]any) error { return nil }) }},
		{name: "put", op: func() error {
			_, err := st.Put(ctx, []query.Record{{Key: "1", Value: map[string]any{}}}, query.Upsert)
			return err
		}},
		{name: "clear", op: func() error { return st.Clear(ctx) }},
		{name: "drop", op: func() error { return st.Drop(ctx) }},
		{name: "typed scan", op: func() error { return st.TypedScan(ctx, func([]query.Record) error { return nil }) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorIs(t, tt.op(), errNoBucket)
		})
	}
}

// TestOpenRejectsABadConnectionOption pins that Open stops when the SDK refuses the
// connection string. The SDK checks the options before it dials, so no server is needed.
// Without the check Open goes on with a nil cluster and panics.
func TestOpenRejectsABadConnectionOption(t *testing.T) {
	_, err := Open(boundedCtx(t), "couchbase://127.0.0.1:1/?kv_timeout=abc", "", nil, numfmt.DecimalAuto)
	require.ErrorContains(t, err, "connect couchbase")
	require.ErrorContains(t, err, "kv_timeout")
	// The message alone cannot tell %w from %v. The SDK error must stay in the chain.
	require.Error(t, errors.Unwrap(err), "the SDK cause must stay reachable through the wrap")
}

// TestOpenKeepsTheReadinessCause pins that the readiness failure stays reachable through
// the wrap. The message alone cannot tell %w from %v.
func TestOpenKeepsTheReadinessCause(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := Open(ctx, "couchbase://Administrator:password@127.0.0.1:1/", "", nil, numfmt.DecimalAuto)
	require.ErrorContains(t, err, "connect couchbase")
	require.ErrorIs(t, err, gocb.ErrRequestCanceled, "the SDK cause must stay reachable through the wrap")
}

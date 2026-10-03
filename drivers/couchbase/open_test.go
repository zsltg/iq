package couchbase

import (
	"context"
	"testing"
	"time"

	"github.com/couchbase/gocb/v2"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

// The tests in this file open a source that no server answers. They need no cluster.

// TestConnectTimeoutIsLiteral pins the readiness bound at 15 seconds. A test that waits
// for the bound would add 15 seconds to every run of the suite, so the test reads the
// constant.
func TestConnectTimeoutIsLiteral(t *testing.T) {
	require.Equal(t, 15*time.Second, connectTimeout)
}

// TestOpenRejectsABadConnectionOption pins that Open stops when the SDK refuses the
// connection string. The SDK checks the options before it dials, so no server is needed.
// Without the check Open goes on with a nil cluster and panics.
func TestOpenRejectsABadConnectionOption(t *testing.T) {
	_, err := Open(context.Background(), "couchbase://127.0.0.1:1/?kv_timeout=abc", "", nil, numfmt.DecimalAuto)
	require.ErrorContains(t, err, "connect couchbase")
	require.ErrorContains(t, err, "kv_timeout")
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

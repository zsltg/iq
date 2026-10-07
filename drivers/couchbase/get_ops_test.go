package couchbase

import (
	"errors"
	"strings"
	"testing"

	"github.com/couchbase/gocb/v2"
	"github.com/stretchr/testify/require"
)

func TestNewGetOps(t *testing.T) {
	ops, err := newGetOps([]string{"b", "a"})
	require.NoError(t, err)
	require.Len(t, ops, 2)
	require.Equal(t, "b", ops[0].ID)
	require.Equal(t, "a", ops[1].ID)

	ops, err = newGetOps([]string{"ok", ""})
	require.ErrorContains(t, err, "document key is empty")
	require.Nil(t, ops)

	ops, err = newGetOps([]string{strings.Repeat("k", 251)})
	require.ErrorContains(t, err, "exceeds 250 bytes")
	require.Nil(t, ops)
}

func TestCollectGetsErrors(t *testing.T) {
	boom := errors.New("boom")
	out := map[string]any{}
	err := (&Store{}).collectGets([]*gocb.GetOp{
		{ID: "gone", Err: gocb.ErrDocumentNotFound},
	}, out)
	require.NoError(t, err)
	require.Empty(t, out, "a missing document is absent")

	err = (&Store{}).collectGets([]*gocb.GetOp{
		{ID: "gone", Err: gocb.ErrDocumentNotFound},
		{ID: "bad", Err: boom},
	}, out)
	require.ErrorIs(t, err, boom)
	require.ErrorContains(t, err, "couchbase get")
}

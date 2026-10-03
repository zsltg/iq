package couchbase

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

// TestInspect exercises the four introspection probes against one seeded collection, so
// the shared collection setup is paid once.
func TestInspect(t *testing.T) {
	st := seedCollection(t, books())
	ctx := skipShort(t)

	t.Run("cluster lists nodes", func(t *testing.T) {
		res, err := st.InspectCluster(ctx)
		require.NoError(t, err)
		nodes := res.(map[string]any)["nodes"].([]any)
		require.NotEmpty(t, nodes)
	})

	t.Run("buckets reports a name, a type and a quota", func(t *testing.T) {
		res, err := st.InspectBuckets(ctx)
		require.NoError(t, err)
		var names []string
		var shared map[string]any
		for _, b := range res.(map[string]any)["buckets"].([]any) {
			entry := b.(map[string]any)
			name := entry["name"].(string)
			names = append(names, name)
			if name == sharedBucket {
				shared = entry
			}
		}
		require.Contains(t, names, sharedBucket)
		// The report is a contract: each of the three fields must be there and carry a
		// value, and the buckets come back in name order.
		require.Contains(t, shared, "type")
		require.NotEmpty(t, shared["type"])
		require.Contains(t, shared, "ramQuotaMB")
		require.NotZero(t, shared["ramQuotaMB"])
		require.True(t, sort.StringsAreSorted(names), "buckets are reported in name order")
	})

	t.Run("collections lists the scope and its collections", func(t *testing.T) {
		res, err := st.InspectCollections(ctx)
		require.NoError(t, err)
		require.Equal(t, sharedBucket, res.(map[string]any)["bucket"])
		var scopes []string
		var def map[string]any
		for _, s := range res.(map[string]any)["scopes"].([]any) {
			entry := s.(map[string]any)
			scope := entry["scope"].(string)
			scopes = append(scopes, scope)
			if scope == defaultScope {
				def = entry
			}
		}
		require.Contains(t, scopes, defaultScope)
		// The collection this test seeded lives in the default scope, so the scope's
		// collection list must name it.
		require.Contains(t, def, "collections")
		require.Contains(t, def["collections"], st.coll)
	})

	t.Run("indexes lists the primary index of this bucket only", func(t *testing.T) {
		res, err := st.InspectIndexes(ctx)
		require.NoError(t, err)
		indexes := res.(map[string]any)["indexes"].([]any)
		require.NotEmpty(t, indexes)
		// A bucket-selected source reports its own bucket's indexes. An index of a
		// bucket-level keyspace carries no bucket_id, so its keyspace_id names the bucket.
		for _, row := range indexes {
			idx := row.(map[string]any)
			owner, ok := idx["bucket_id"]
			if !ok {
				owner = idx["keyspace_id"]
			}
			require.Equal(t, sharedBucket, owner, "a bucket-scoped index list is filtered to that bucket")
		}
	})
}

// TestInspectContextCancelled pins that the context each probe is handed reaches its
// request: a context cancelled before the probe starts must surface the cancellation
// instead of a completed read. It also pins that each probe names itself in the error.
func TestInspectContextCancelled(t *testing.T) {
	ctx := skipShort(t)
	st := openIntegration(t, collName(t))
	cctx, cancel := context.WithCancel(ctx)
	cancel() // cancel before the first probe issues its request

	tests := []struct {
		name string
		op   func() error
		want string
	}{
		{
			name: "cluster",
			op:   func() error { _, err := st.InspectCluster(cctx); return err },
			want: "couchbase inspect cluster",
		},
		{
			name: "buckets",
			op:   func() error { _, err := st.InspectBuckets(cctx); return err },
			want: "couchbase inspect buckets",
		},
		{
			name: "collections",
			op:   func() error { _, err := st.InspectCollections(cctx); return err },
			want: "couchbase inspect collections",
		},
		{
			name: "indexes",
			op:   func() error { _, err := st.InspectIndexes(cctx); return err },
			want: "couchbase inspect indexes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.op()
			require.ErrorContains(t, err, tt.want,
				"a cancelled context must surface as an error, not a completed probe")
			// The message alone cannot tell %w from %v. The SDK cause must stay in the chain.
			require.Error(t, errors.Unwrap(err), "the SDK cause must stay reachable through the wrap")
		})
	}
}

// TestInspectIndexesNoBucket confirms a bucket-less source lists the cluster's indexes
// unscoped: it must return the seeded primary index rather than filter to an empty
// bucket, pinning that the bucket filter is applied only when a bucket is selected.
func TestInspectIndexesNoBucket(t *testing.T) {
	seedCollection(t, books()) // creates a primary index that lives for the test
	ctx := skipShort(t)
	nb, err := Open(ctx, sharedBase, "", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = nb.Close() })

	res, err := nb.InspectIndexes(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, res.(map[string]any)["indexes"])
}

// TestInspectIndexesFilterDropsOtherBuckets pins the bucket filter with a store that
// names a bucket the cluster does not have. The test cluster holds one bucket, so a
// filter that removes nothing cannot show on the real name. The cluster does hold an
// index, and none of it belongs to the missing bucket, so the list must be empty.
func TestInspectIndexesFilterDropsOtherBuckets(t *testing.T) {
	st := seedCollection(t, books()) // creates a primary index that lives for the test
	ctx := skipShort(t)

	other := *st
	other.bucket = "iq_no_such_bucket"
	res, err := other.InspectIndexes(ctx)
	require.NoError(t, err)
	require.Empty(t, res.(map[string]any)["indexes"], "an index of another bucket must not appear")
}

// TestInspectCollectionsNoBucket confirms the bucket-scoped probe reports a missing
// bucket rather than panicking.
func TestInspectCollectionsNoBucket(t *testing.T) {
	ctx := skipShort(t)
	st, err := Open(ctx, sharedBase, "", nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	_, err = st.InspectCollections(ctx)
	require.ErrorIs(t, err, errNoBucket)
}

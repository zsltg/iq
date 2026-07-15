package couchbase

import (
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

	t.Run("buckets includes the shared bucket", func(t *testing.T) {
		res, err := st.InspectBuckets(ctx)
		require.NoError(t, err)
		var names []string
		for _, b := range res.(map[string]any)["buckets"].([]any) {
			names = append(names, b.(map[string]any)["name"].(string))
		}
		require.Contains(t, names, sharedBucket)
	})

	t.Run("collections lists the default scope", func(t *testing.T) {
		res, err := st.InspectCollections(ctx)
		require.NoError(t, err)
		require.Equal(t, sharedBucket, res.(map[string]any)["bucket"])
		var scopes []string
		for _, s := range res.(map[string]any)["scopes"].([]any) {
			scopes = append(scopes, s.(map[string]any)["scope"].(string))
		}
		require.Contains(t, scopes, defaultScope)
	})

	t.Run("indexes lists the primary index", func(t *testing.T) {
		res, err := st.InspectIndexes(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, res.(map[string]any)["indexes"])
	})
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

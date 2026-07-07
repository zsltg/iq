package neo4jenvelope_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/neo4jenvelope"
)

// TestFieldNames pins the reserved envelope keys: they are a frozen wire contract
// shared by the live Bolt driver and the offline APOC-JSON reader, so a rename here
// would silently diverge the two and break every jq filter that references them.
func TestFieldNames(t *testing.T) {
	require.Equal(t, "_id", neo4jenvelope.FieldID)
	require.Equal(t, "_labels", neo4jenvelope.FieldLabels)
	require.Equal(t, "_type", neo4jenvelope.FieldType)
	require.Equal(t, "_start", neo4jenvelope.FieldStart)
	require.Equal(t, "_end", neo4jenvelope.FieldEnd)
}

func TestIsReserved(t *testing.T) {
	for _, k := range []string{"_id", "_labels", "_type", "_start", "_end"} {
		require.Truef(t, neo4jenvelope.IsReserved(k), "reserved key %q", k)
	}
	for _, k := range []string{"id", "name", "_ID", "type", ""} {
		require.Falsef(t, neo4jenvelope.IsReserved(k), "non-reserved key %q", k)
	}
}

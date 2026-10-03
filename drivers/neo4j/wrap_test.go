package neo4j

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseURLErrorKeepsItsCause(t *testing.T) {
	_, err := parseURL("neo4j://host/\x7f", "")
	var urlErr *url.Error
	require.ErrorAs(t, err, &urlErr)
}

func TestIntegrationEstimateCountReturnsZeroWithAnError(t *testing.T) {
	ctx := integrationOrSkip(t)
	st, err := Open(ctx, testURL()+"?database=it_no_such_db&label=X", "", nil, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	n, err := st.EstimateCount(ctx)
	require.Error(t, err)
	require.Zero(t, n)
}

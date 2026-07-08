package query_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestNonObjectValueError(t *testing.T) {
	err := query.NonObjectValueError("mongodb", "k2")
	require.ErrorContains(t, err, "mongodb")
	require.ErrorContains(t, err, `key "k2"`)
	require.ErrorContains(t, err, "is not a JSON object")
	require.ErrorContains(t, err, "transform explicitly")
}

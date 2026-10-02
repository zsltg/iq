package dynamodb

import (
	"context"
	"errors"
	"net/url"
	"testing"

	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestConnectionErrorsKeepTheirCause(t *testing.T) {
	t.Run("url that does not parse", func(t *testing.T) {
		_, err := parseURL("dynamodb://us-east-1/\x7f", "")
		var urlErr *url.Error
		require.ErrorAs(t, err, &urlErr)
	})
	t.Run("aws config", func(t *testing.T) {
		t.Setenv("AWS_RETRY_MODE", "bogus")
		_, err := Open(context.Background(), "dynamodb://us-east-1/?endpoint=http://127.0.0.1:1", "", nil, numfmt.DecimalAuto)
		require.Error(t, errors.Unwrap(err), "the config failure must stay in the chain")
	})
}

func TestEstimateCountReturnsZeroWithAnError(t *testing.T) {
	t.Run("no table", func(t *testing.T) {
		s := &Store{client: &fakeDDB{}, table: "t"}
		n, err := s.EstimateCount(context.Background())
		require.ErrorIs(t, err, errNoTable)
		require.Zero(t, n)
	})
	t.Run("describe fails", func(t *testing.T) {
		fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
			return nil, errBoom
		}}
		n, err := fakeStore(t, fake).EstimateCount(context.Background())
		require.ErrorIs(t, err, errBoom)
		require.Zero(t, n)
	})
}

func TestExistingKeySetRejectsAMalformedKey(t *testing.T) {
	fake := &fakeDDB{}
	got, err := fakeStore(t, fake).existingKeySet(context.Background(), []string{"not-a-number"})
	require.ErrorContains(t, err, "is not a number")
	require.Nil(t, got)
}

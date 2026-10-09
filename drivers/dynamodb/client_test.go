package dynamodb

import (
	"context"
	"strings"
	"testing"

	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/stretchr/testify/require"
)

func TestClientOption(t *testing.T) {
	tests := []struct {
		name         string
		cc           connConfig
		trace        *strings.Builder
		wantEndpoint string
		wantAPIOpts  int
	}{
		{"neither endpoint nor trace", connConfig{}, nil, "", 0},
		{"endpoint only", connConfig{endpoint: "http://127.0.0.1:8000"}, nil, "http://127.0.0.1:8000", 0},
		{"trace only", connConfig{}, &strings.Builder{}, "", 1},
		{"endpoint and trace", connConfig{endpoint: "http://127.0.0.1:8000"}, &strings.Builder{}, "http://127.0.0.1:8000", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var o awsdynamodb.Options
			if tt.trace == nil {
				clientOption(tt.cc, nil)(&o)
			} else {
				clientOption(tt.cc, tt.trace)(&o)
			}
			if tt.wantEndpoint == "" {
				require.Nil(t, o.BaseEndpoint)
			} else {
				require.NotNil(t, o.BaseEndpoint)
				require.Equal(t, tt.wantEndpoint, *o.BaseEndpoint)
			}
			require.Len(t, o.APIOptions, tt.wantAPIOpts)
		})
	}
}

func TestNewClient(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Run("a custom endpoint gets dummy credentials and the region", func(t *testing.T) {
		c, err := newClient(context.Background(), connConfig{region: "eu-west-2", endpoint: "http://127.0.0.1:1"}, nil)
		require.NoError(t, err)
		require.Equal(t, "eu-west-2", c.Options().Region)
		require.Equal(t, "http://127.0.0.1:1", *c.Options().BaseEndpoint)
		creds, err := c.Options().Credentials.Retrieve(context.Background())
		require.NoError(t, err)
		require.Equal(t, "dummy", creds.AccessKeyID)
		require.Equal(t, "dummy", creds.SecretAccessKey)
	})
	t.Run("an unreadable config is wrapped", func(t *testing.T) {
		t.Setenv("AWS_RETRY_MODE", "bogus")
		_, err := newClient(context.Background(), connConfig{region: "us-east-1"}, nil)
		require.ErrorContains(t, err, "load aws config")
	})
}

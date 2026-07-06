package dynamodb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		address      string
		wantRegion   string
		wantTable    string
		wantEndpoint string
		wantErr      string
	}{
		{
			name:       "region and table",
			url:        "dynamodb://us-east-1/?table=books",
			wantRegion: "us-east-1",
			wantTable:  "books",
		},
		{
			name:         "endpoint override for local",
			url:          "dynamodb://us-east-1/?table=books&endpoint=http://localhost:8000",
			wantRegion:   "us-east-1",
			wantTable:    "books",
			wantEndpoint: "http://localhost:8000",
		},
		{
			name:       "address overrides table param",
			url:        "dynamodb://eu-west-2/?table=books",
			address:    "orders",
			wantRegion: "eu-west-2",
			wantTable:  "orders",
		},
		{
			name:       "no table is valid for raw-only use",
			url:        "dynamodb://us-east-1/",
			wantRegion: "us-east-1",
			wantTable:  "",
		},
		{
			name:    "missing region",
			url:     "dynamodb:///?table=books",
			wantErr: "must name a region",
		},
		{
			name:    "wrong scheme",
			url:     "cassandra://h/ks",
			wantErr: "must start with dynamodb://",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, err := parseURL(tt.url, tt.address)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantRegion, cc.region)
			require.Equal(t, tt.wantTable, cc.table)
			require.Equal(t, tt.wantEndpoint, cc.endpoint)
		})
	}
}

func TestTarget(t *testing.T) {
	region, table, err := Target("dynamodb://ap-south-1/?table=books", "")
	require.NoError(t, err)
	require.Equal(t, "ap-south-1", region)
	require.Equal(t, "books", table)

	// The address override wins over the URL default.
	_, table, err = Target("dynamodb://ap-south-1/?table=books", "orders")
	require.NoError(t, err)
	require.Equal(t, "orders", table)

	// A parse error propagates.
	_, _, err = Target("redis://h", "")
	require.Error(t, err)
}

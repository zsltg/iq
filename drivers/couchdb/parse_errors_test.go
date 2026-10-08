package couchdb

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestParseURLErrorTexts(t *testing.T) {
	// Each rejection has its exact text, and none of them leaks the password.
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "wrong scheme",
			url:  "http://user:s3cret@host:5984/",
			want: `couchdb url must use couchdb:// or couchdbs://, got "http"`,
		},
		{
			name: "missing host",
			url:  "couchdb://user:s3cret@/?database=iq",
			want: "couchdb url must name a host, e.g. couchdb://localhost:5984/?database=mydb",
		},
		{
			name: "no scheme",
			url:  "user:s3cret@host:5984/",
			want: `couchdb url must use couchdb:// or couchdbs://, got "user"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, err := parseURL(tt.url, "")
			require.Equal(t, connConfig{}, cc)
			require.EqualError(t, err, tt.want)
			require.NotContains(t, err.Error(), "s3cret")

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err = Open(ctx, tt.url, "", nil, numfmt.DecimalAuto)
			require.EqualError(t, err, tt.want)
			require.NotContains(t, err.Error(), "s3cret")
		})
	}
}

func TestParseURLDatabasePrecedence(t *testing.T) {
	// The address beats the param, the param beats the path, and the path counts
	// only when it is a single segment.
	tests := []struct {
		name    string
		url     string
		address string
		wantDSN string
		wantDB  string
	}{
		{"param beats path", "couchdb://host:5984/pathdb?database=paramdb", "", "http://host:5984/", "paramdb"},
		{"trailing slash path", "couchdb://host:5984/mydb/", "", "http://host:5984/", "mydb"},
		{"empty param falls back to the path", "couchdb://host:5984/mydb?database=", "", "http://host:5984/", "mydb"},
		{"param beats a multi-segment path", "couchdb://host:5984/a/b?database=paramdb", "", "http://host:5984/", "paramdb"},
		{"tls with userinfo", "couchdbs://u:p@host:6984/?database=shop", "", "https://u:p@host:6984/", "shop"},
		{"address beats param and path", "couchdb://host:5984/pathdb?database=paramdb", "addr", "http://host:5984/", "addr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, err := parseURL(tt.url, tt.address)
			require.NoError(t, err)
			require.Equal(t, connConfig{dsn: tt.wantDSN, db: tt.wantDB}, cc)
		})
	}
}

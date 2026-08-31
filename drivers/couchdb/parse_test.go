package couchdb

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		address string
		wantDSN string
		wantDB  string
		wantErr bool
	}{
		{
			name:    "database param",
			url:     "couchdb://localhost:5984/?database=iq",
			wantDSN: "http://localhost:5984/",
			wantDB:  "iq",
		},
		{
			name:    "tls scheme maps to https",
			url:     "couchdbs://host:6984/?database=shop",
			wantDSN: "https://host:6984/",
			wantDB:  "shop",
		},
		{
			name:    "userinfo is preserved in the dsn",
			url:     "couchdb://admin:secret@host:5984/?database=iq",
			wantDSN: "http://admin:secret@host:5984/",
			wantDB:  "iq",
		},
		{
			name:    "path names the database (idiomatic form)",
			url:     "couchdb://host:5984/mydb",
			wantDSN: "http://host:5984/",
			wantDB:  "mydb",
		},
		{
			name:    "dotted address overrides the database param",
			url:     "couchdb://host:5984/?database=iq",
			address: "other",
			wantDSN: "http://host:5984/",
			wantDB:  "other",
		},
		{
			name:    "dotted address overrides the path database",
			url:     "couchdb://host:5984/mydb",
			address: "other",
			wantDB:  "other",
			wantDSN: "http://host:5984/",
		},
		{
			name:    "server-only url has no database",
			url:     "couchdb://host:5984/",
			wantDSN: "http://host:5984/",
			wantDB:  "",
		},
		{
			name:    "multi-segment path is not a database",
			url:     "couchdb://host:5984/a/b",
			wantDSN: "http://host:5984/",
			wantDB:  "",
		},
		{
			name:    "wrong scheme is rejected",
			url:     "http://host:5984/",
			wantErr: true,
		},
		{
			name:    "missing host is rejected",
			url:     "couchdb:///?database=iq",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, err := parseURL(tt.url, tt.address)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantDSN, cc.dsn)
			require.Equal(t, tt.wantDB, cc.db)
		})
	}
}

func TestParseURLWrapsTheURLFailure(t *testing.T) {
	// The parser's own failure is wrapped, not flattened to a string, so a caller
	// can still reach the url package's error.
	cc, err := parseURL("couchdb://host:5984/%zz", "")
	require.Equal(t, connConfig{}, cc)
	require.ErrorContains(t, err, "parse couchdb url")

	var urlErr *url.Error
	require.ErrorAs(t, err, &urlErr)
}

func TestOpenRejectsAMalformedURL(t *testing.T) {
	// A bad source URL fails at parse time with the parser's own message, before
	// any connection is attempted, so the user is told what is wrong with the URL
	// rather than that some server is unreachable.
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "wrong scheme",
			url:  "http://host:5984/",
			want: "couchdb url must use couchdb:// or couchdbs://",
		},
		{
			name: "missing host",
			url:  "couchdb:///?database=iq",
			want: "couchdb url must name a host",
		},
		{
			name: "unparsable url",
			url:  "couchdb://host:5984/%zz",
			want: "parse couchdb url",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st, err := Open(ctx, tt.url, "", nil, numfmt.DecimalAuto)
			require.Nil(t, st)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

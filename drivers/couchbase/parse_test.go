package couchbase

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		name        string
		rawURL      string
		address     string
		wantConnStr string
		wantUser    string
		wantPass    string
		wantBucket  string
		wantScope   string
		wantColl    string
	}{
		{
			name:        "bucket only defaults to default collection",
			rawURL:      "couchbase://user:pass@localhost/?bucket=iq",
			wantConnStr: "couchbase://localhost",
			wantUser:    "user",
			wantPass:    "pass",
			wantBucket:  "iq",
			wantScope:   "_default",
			wantColl:    "_default",
		},
		{
			name:        "collection param, default scope",
			rawURL:      "couchbase://u:p@localhost/?bucket=iq&collection=orders",
			wantConnStr: "couchbase://localhost",
			wantUser:    "u",
			wantPass:    "p",
			wantBucket:  "iq",
			wantScope:   "_default",
			wantColl:    "orders",
		},
		{
			name:        "scope.collection param",
			rawURL:      "couchbase://u:p@localhost/?bucket=iq&collection=sales.orders",
			wantConnStr: "couchbase://localhost",
			wantUser:    "u",
			wantPass:    "p",
			wantBucket:  "iq",
			wantScope:   "sales",
			wantColl:    "orders",
		},
		{
			name:        "dotted address overrides collection param",
			rawURL:      "couchbase://u:p@localhost/?bucket=iq&collection=orders",
			address:     "archive",
			wantConnStr: "couchbase://localhost",
			wantUser:    "u",
			wantPass:    "p",
			wantBucket:  "iq",
			wantScope:   "_default",
			wantColl:    "archive",
		},
		{
			name:        "dotted address with scope",
			rawURL:      "couchbase://u:p@localhost/?bucket=iq",
			address:     "sales.orders",
			wantConnStr: "couchbase://localhost",
			wantUser:    "u",
			wantPass:    "p",
			wantBucket:  "iq",
			wantScope:   "sales",
			wantColl:    "orders",
		},
		{
			name:        "tls scheme preserved",
			rawURL:      "couchbases://u:p@localhost/?bucket=iq",
			wantConnStr: "couchbases://localhost",
			wantUser:    "u",
			wantPass:    "p",
			wantBucket:  "iq",
			wantScope:   "_default",
			wantColl:    "_default",
		},
		{
			name:        "extra query kept as connstr option, iq params stripped",
			rawURL:      "couchbase://u:p@localhost/?bucket=iq&kv_timeout=10s",
			wantConnStr: "couchbase://localhost?kv_timeout=10s",
			wantUser:    "u",
			wantPass:    "p",
			wantBucket:  "iq",
			wantScope:   "_default",
			wantColl:    "_default",
		},
		{
			name:        "no bucket is allowed (raw/inspect only)",
			rawURL:      "couchbase://u:p@localhost/",
			wantConnStr: "couchbase://localhost",
			wantUser:    "u",
			wantPass:    "p",
			wantBucket:  "",
			wantScope:   "_default",
			wantColl:    "_default",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, err := parseURL(tt.rawURL, tt.address)
			require.NoError(t, err)
			require.Equal(t, tt.wantConnStr, cc.connStr)
			require.Equal(t, tt.wantUser, cc.username)
			require.Equal(t, tt.wantPass, cc.password)
			require.Equal(t, tt.wantBucket, cc.bucket)
			require.Equal(t, tt.wantScope, cc.scope)
			require.Equal(t, tt.wantColl, cc.coll)
		})
	}
}

func TestParseURLErrors(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		address string
		want    string
	}{
		// An invalid percent escape makes url.Parse fail. Without the guard the next line
		// reads a nil URL and panics, so the test also proves the failure is an error.
		{name: "malformed url", rawURL: "couchbase://localhost/%zz?bucket=iq", want: "parse couchbase url"},
		{name: "wrong scheme", rawURL: "mongodb://localhost/?bucket=iq"},
		{name: "no host", rawURL: "couchbase:///?bucket=iq"},
		// Each segment is a valid name, so only the segment-count check can reject this
		// spec. The message pins that check: without it the name checks still fail.
		{name: "too many collection segments", rawURL: "couchbase://localhost/?bucket=iq&collection=a.b.c", want: "must be collection or scope.collection"},
		{name: "hostile bucket backtick", rawURL: "couchbase://localhost/?bucket=iq`drop"},
		{name: "hostile collection space", rawURL: "couchbase://localhost/?bucket=iq&collection=a b"},
		{name: "hostile scope quote", rawURL: "couchbase://localhost/?bucket=iq", address: "a'b.c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseURL(tt.rawURL, tt.address)
			require.Error(t, err)
			if tt.want != "" {
				require.ErrorContains(t, err, tt.want)
			}
		})
	}
}

// TestNameLimitsAreLiteral pins the documented Couchbase limits with literal numbers. The
// other limit tests build their input from the constants, so a changed constant moves the
// input with it and the test still passes.
func TestNameLimitsAreLiteral(t *testing.T) {
	ident := func(s string) error { return validateIdent("bucket", s) }
	tests := []struct {
		name    string
		check   func(string) error
		length  int
		wantErr bool
	}{
		{name: "identifier of 251 bytes", check: ident, length: 251},
		{name: "identifier of 252 bytes", check: ident, length: 252, wantErr: true},
		{name: "key of 250 bytes", check: validateKey, length: 250},
		{name: "key of 251 bytes", check: validateKey, length: 251, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.check(strings.Repeat("a", tt.length))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestParseURLKeepsTheParseCause pins that the url.Parse failure stays in the error chain.
// The message alone cannot tell %w from %v.
func TestParseURLKeepsTheParseCause(t *testing.T) {
	_, err := parseURL("couchbase://localhost/%zz?bucket=iq", "")
	var urlErr *url.Error
	require.ErrorAs(t, err, &urlErr)
}

func TestValidateKey(t *testing.T) {
	require.NoError(t, validateKey("k1"))
	require.NoError(t, validateKey(strings.Repeat("k", maxKeyBytes)), "exactly at the limit is valid")
	require.Error(t, validateKey(""))
	require.Error(t, validateKey(strings.Repeat("k", maxKeyBytes+1)), "one over the limit is rejected")
}

func TestValidateIdent(t *testing.T) {
	require.NoError(t, validateIdent("bucket", "iq_test-1%"))
	require.NoError(t, validateIdent("collection", "_default"))
	require.NoError(t, validateIdent("bucket", "azAZ09"), "both ends of each accepted range are valid")
	require.NoError(t, validateIdent("bucket", "has.dot"), "a dot is an accepted character")
	require.NoError(t, validateIdent("bucket", strings.Repeat("a", maxIdentBytes)), "exactly at the limit is valid")
	require.Error(t, validateIdent("bucket", ""))
	require.Error(t, validateIdent("bucket", strings.Repeat("a", maxIdentBytes+1)), "one over the limit is rejected")
	require.Error(t, validateIdent("bucket", "has`tick"))
	require.Error(t, validateIdent("bucket", "has space"))
	// The character directly above each accepted range. A range that reaches too far
	// accepts one of these and lets an unexpected character into a keyspace name.
	require.Error(t, validateIdent("bucket", "has{brace"), "the character above the lowercase range is rejected")
	require.Error(t, validateIdent("bucket", "has[bracket"), "the character above the uppercase range is rejected")
	require.Error(t, validateIdent("bucket", "has:colon"), "the character above the digit range is rejected")
}

package elasticsearch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		name       string
		rawURL     string
		address    string
		wantFlavor flavor
		wantAddr   string
		wantUser   string
		wantPass   string
		wantIdx    string
		wantErr    string
	}{
		{
			name:       "host and index query",
			rawURL:     "elasticsearch://localhost:9200/?index=books",
			wantFlavor: flavorES,
			wantAddr:   "http://localhost:9200",
			wantIdx:    "books",
		},
		{
			name:       "tls scheme maps to https",
			rawURL:     "elasticsearch+s://es.example:9200/?index=books",
			wantFlavor: flavorES,
			wantAddr:   "https://es.example:9200",
			wantIdx:    "books",
		},
		{
			name:       "opensearch scheme selects the opensearch flavor",
			rawURL:     "opensearch://localhost:9200/?index=books",
			wantFlavor: flavorOS,
			wantAddr:   "http://localhost:9200",
			wantIdx:    "books",
		},
		{
			name:       "opensearch tls scheme maps to https",
			rawURL:     "opensearch+s://os.example:9200/?index=books",
			wantFlavor: flavorOS,
			wantAddr:   "https://os.example:9200",
			wantIdx:    "books",
		},
		{
			name:     "userinfo becomes basic auth",
			rawURL:   "elasticsearch://elastic:secret@localhost:9200/?index=books",
			wantAddr: "http://localhost:9200",
			wantUser: "elastic",
			wantPass: "secret",
			wantIdx:  "books",
		},
		{
			name:     "dotted address overrides index query",
			rawURL:   "elasticsearch://localhost:9200/?index=books",
			address:  "authors",
			wantAddr: "http://localhost:9200",
			wantIdx:  "authors",
		},
		{
			name:     "index in path when no query",
			rawURL:   "elasticsearch://localhost:9200/books",
			wantAddr: "http://localhost:9200",
			wantIdx:  "books",
		},
		{
			name:     "no index is allowed (raw/inspect-only)",
			rawURL:   "elasticsearch://localhost:9200/",
			wantAddr: "http://localhost:9200",
			wantIdx:  "",
		},
		{
			name:    "wrong scheme",
			rawURL:  "http://localhost:9200/?index=books",
			wantErr: "must use elasticsearch://",
		},
		{
			name:    "missing host",
			rawURL:  "elasticsearch:///?index=books",
			wantErr: "must name a host",
		},
		{
			name:    "invalid index rejected",
			rawURL:  "elasticsearch://localhost:9200/?index=Books",
			wantErr: "must be lowercase",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, err := parseURL(tt.rawURL, tt.address)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantFlavor, cc.flavor)
			require.Equal(t, tt.wantAddr, cc.addr)
			require.Equal(t, tt.wantUser, cc.username)
			require.Equal(t, tt.wantPass, cc.password)
			require.Equal(t, tt.wantIdx, cc.index)
		})
	}
}

func TestValidateIndex(t *testing.T) {
	tests := []struct {
		name    string
		index   string
		wantErr string
	}{
		{name: "simple", index: "books"},
		{name: "with underscore mid", index: "iq_test_books"},
		{name: "with digits", index: "books2025"},
		{name: "255 bytes is allowed", index: strings.Repeat("a", 255)},
		{name: "256 bytes is rejected", index: strings.Repeat("a", 256), wantErr: "exceeds 255 bytes"},
		{name: "empty", index: "", wantErr: "is empty"},
		{name: "uppercase", index: "Books", wantErr: "must be lowercase"},
		{name: "wildcard", index: "book*", wantErr: "invalid character"},
		{name: "comma multi-index", index: "a,b", wantErr: "invalid character"},
		{name: "slash", index: "a/b", wantErr: "invalid character"},
		{name: "space", index: "a b", wantErr: "invalid character"},
		{name: "leading underscore", index: "_books", wantErr: "must not start"},
		{name: "leading dash", index: "-books", wantErr: "must not start"},
		{name: "dot", index: ".", wantErr: "is invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateIndex(tt.index)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

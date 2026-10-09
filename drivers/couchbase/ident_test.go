package couchbase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateIdentTable pins the accepted set, the rejected neighbours, the error
// texts and the check order of validateIdent.
func TestValidateIdentTable(t *testing.T) {
	invalid := func(kind identKind, name string) string {
		return fmt.Sprintf("couchbase %s name %q has an invalid character", kind, name)
	}
	tests := []struct {
		name string
		kind identKind
		in   string
		want string
	}{
		{name: "lower a", kind: "bucket", in: "a"},
		{name: "upper Z", kind: "bucket", in: "Z"},
		{name: "digit 0", kind: "bucket", in: "0"},
		{name: "underscore", kind: "bucket", in: "_"},
		{name: "hyphen", kind: "bucket", in: "-"},
		{name: "percent", kind: "bucket", in: "%"},
		{name: "dot", kind: "bucket", in: "."},
		{name: "dotted", kind: "bucket", in: "a.b"},
		{name: "mixed", kind: "bucket", in: "iq_test-1%"},
		{name: "reserved word", kind: "bucket", in: "select"},
		{name: "longest", kind: "bucket", in: strings.Repeat("a", 251)},
		{name: "empty", kind: "bucket", in: "", want: "couchbase bucket name is empty"},
		{name: "empty scope", kind: "scope", in: "", want: "couchbase scope name is empty"},
		{name: "too long", kind: "bucket", in: strings.Repeat("a", 252), want: "couchbase bucket name exceeds 251 bytes"},
		{name: "length counts bytes", kind: "collection", in: strings.Repeat("é", 126), want: "couchbase collection name exceeds 251 bytes"},
		{name: "backtick", kind: "bucket", in: "a`b", want: invalid("bucket", "a`b")},
		{name: "backslash", kind: "bucket", in: "a\\b", want: invalid("bucket", "a\\b")},
		{name: "space", kind: "bucket", in: "a b", want: invalid("bucket", "a b")},
		{name: "single quote", kind: "bucket", in: "a'b", want: invalid("bucket", "a'b")},
		{name: "double quote", kind: "bucket", in: "a\"b", want: invalid("bucket", "a\"b")},
		{name: "slash", kind: "bucket", in: "a/b", want: invalid("bucket", "a/b")},
		{name: "comma", kind: "bucket", in: "a,b", want: invalid("bucket", "a,b")},
		{name: "plus", kind: "bucket", in: "a+b", want: invalid("bucket", "a+b")},
		{name: "colon", kind: "bucket", in: "a:b", want: invalid("bucket", "a:b")},
		{name: "dollar", kind: "bucket", in: "a$b", want: invalid("bucket", "a$b")},
		{name: "at sign", kind: "bucket", in: "a@b", want: invalid("bucket", "a@b")},
		{name: "open brace", kind: "bucket", in: "a{b", want: invalid("bucket", "a{b")},
		{name: "open bracket", kind: "scope", in: "a[b", want: invalid("scope", "a[b")},
		{name: "NUL", kind: "bucket", in: "a\x00b", want: invalid("bucket", "a\x00b")},
		{name: "newline", kind: "bucket", in: "a\nb", want: invalid("bucket", "a\nb")},
		{name: "accented letter", kind: "bucket", in: "é", want: invalid("bucket", "é")},
		{name: "CJK", kind: "bucket", in: "桶", want: invalid("bucket", "桶")},
		{name: "emoji", kind: "bucket", in: "a😀", want: invalid("bucket", "a😀")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateIdent(tt.kind, tt.in)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.want)
		})
	}
}

// TestQueryArgumentErrors pins the argument checks of Query. They return before any
// cluster use, so a zero Store runs them.
func TestQueryArgumentErrors(t *testing.T) {
	st := &Store{}
	_, err := st.Query(context.Background(), nil)
	require.ErrorContains(t, err, "couchbase raw expects a SQL++ statement")
	_, err = st.Query(context.Background(), []string{"a", "{}", "c"})
	require.ErrorContains(t, err, "couchbase raw expects a SQL++ statement")
	_, err = st.Query(context.Background(), []string{"a", "{"})
	require.ErrorContains(t, err, "parse couchbase query parameters")
	var syn *json.SyntaxError
	require.ErrorAs(t, err, &syn)
}

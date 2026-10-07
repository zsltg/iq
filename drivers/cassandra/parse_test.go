package cassandra

import (
	"fmt"
	"testing"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/stretchr/testify/require"
)

func TestParseURL(t *testing.T) {
	t.Run("host keyspace and table param", func(t *testing.T) {
		cc, err := parseURL("cassandra://localhost:9042/shop?table=orders", "")
		require.NoError(t, err)
		require.Equal(t, []string{"localhost:9042"}, cc.hosts)
		require.Equal(t, "shop", cc.keyspace)
		require.Equal(t, "orders", cc.table)
		require.Equal(t, gocql.Quorum, cc.consistency)
	})
	t.Run("bare host gets default port", func(t *testing.T) {
		cc, err := parseURL("cassandra://localhost/shop", "")
		require.NoError(t, err)
		require.Equal(t, []string{"localhost:9042"}, cc.hosts)
	})
	t.Run("address overrides table param", func(t *testing.T) {
		cc, err := parseURL("cassandra://h/shop?table=orders", "override")
		require.NoError(t, err)
		require.Equal(t, "override", cc.table)
	})
	t.Run("multiple hosts", func(t *testing.T) {
		cc, err := parseURL("cassandra://n1,n2:9042/shop", "")
		require.NoError(t, err)
		require.Equal(t, []string{"n1:9042", "n2:9042"}, cc.hosts)
	})
	t.Run("userinfo", func(t *testing.T) {
		cc, err := parseURL("cassandra://alice:s3cret@h/shop", "")
		require.NoError(t, err)
		require.Equal(t, "alice", cc.username)
		require.Equal(t, "s3cret", cc.password)
	})
	userinfoTests := []struct {
		name     string
		uri      string
		user     string
		password string
		hosts    []string
		wantErr  error
	}{
		{"password with at sign and colon", "cassandra://u:ab@cd:ef@host/ks", "u", "ab@cd:ef", []string{"host:9042"}, nil},
		{"percent-encoded user and password", "cassandra://u%40x:p%40ss@host/ks", "u@x", "p@ss", []string{"host:9042"}, nil},
		{"empty userinfo", "cassandra://@host/ks", "", "", []string{"host:9042"}, nil},
		{"bad escape in password", "cassandra://u:dummysecret%zz@host/ks", "", "", nil, errBadUserinfo},
		{"bad escape in user name", "cassandra://u%zz:p@host/ks", "", "", nil, errBadUserinfo},
	}
	for _, tt := range userinfoTests {
		t.Run(tt.name, func(t *testing.T) {
			cc, err := parseURL(tt.uri, "")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.NotContains(t, err.Error(), "dummysecret")
				require.NotContains(t, err.Error(), "u%zz")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.user, cc.username)
			require.Equal(t, tt.password, cc.password)
			require.Equal(t, tt.hosts, cc.hosts)
		})
	}
	t.Run("consistency param", func(t *testing.T) {
		cc, err := parseURL("cassandra://h/shop?consistency=local_quorum", "")
		require.NoError(t, err)
		require.Equal(t, gocql.LocalQuorum, cc.consistency)
	})
	t.Run("wrong scheme", func(t *testing.T) {
		_, err := parseURL("redis://h/shop", "")
		require.ErrorContains(t, err, "must start with cassandra://")
	})
	t.Run("missing keyspace", func(t *testing.T) {
		_, err := parseURL("cassandra://h/", "")
		require.ErrorContains(t, err, "must name a keyspace")
	})
	t.Run("missing host", func(t *testing.T) {
		_, err := parseURL("cassandra:///shop", "")
		require.ErrorContains(t, err, "must name at least one host")
	})
	t.Run("unknown consistency", func(t *testing.T) {
		_, err := parseURL("cassandra://h/shop?consistency=bogus", "")
		require.ErrorContains(t, err, "unknown consistency")
	})
}

func TestWithPort(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare host", "localhost", "localhost:9042"},
		{"host with port", "localhost:1234", "localhost:1234"},
		{"empty stays empty", "", ""},
		{"ipv6 without port", "[::1]", "[::1]:9042"},
		{"ipv6 with port", "[::1]:1234", "[::1]:1234"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, withPort(tt.in))
		})
	}
}

func TestParseConsistency(t *testing.T) {
	tests := []struct {
		in   string
		want gocql.Consistency
	}{
		{"", gocql.Quorum},
		{"one", gocql.One},
		{"QUORUM", gocql.Quorum},
		{"all", gocql.All},
		{"local_quorum", gocql.LocalQuorum},
		{"localone", gocql.LocalOne},
		{"each_quorum", gocql.EachQuorum},
		{"any", gocql.Any},
		{"two", gocql.Two},
		{"three", gocql.Three},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseConsistency(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
	t.Run("unknown errors", func(t *testing.T) {
		_, err := parseConsistency("nope")
		require.ErrorContains(t, err, "unknown consistency")
	})
}

func TestQuoteIdent(t *testing.T) {
	require.Equal(t, `"books"`, quoteIdent("books"))
	require.Equal(t, `"a""b"`, quoteIdent(`a"b`))
}

func TestAuthenticatorFor(t *testing.T) {
	require.Nil(t, authenticatorFor(connConfig{}), "no username means anonymous")
	require.Equal(t,
		gocql.PasswordAuthenticator{Username: "alice", Password: "s3cret"},
		authenticatorFor(connConfig{username: "alice", password: "s3cret"}))
}

func TestTarget(t *testing.T) {
	ks, table, err := Target("cassandra://h/shop?table=orders", "")
	require.NoError(t, err)
	require.Equal(t, "shop", ks)
	require.Equal(t, "orders", table)

	_, _, err = Target("redis://h/x", "")
	require.Error(t, err)
}

// consistencyNameCases lists every name that parseConsistency accepts, and a few
// spellings that need trimming or case folding.
var consistencyNameCases = []struct {
	in   string
	want gocql.Consistency
}{
	{"any", gocql.Any},
	{"one", gocql.One},
	{"two", gocql.Two},
	{"three", gocql.Three},
	{"quorum", gocql.Quorum},
	{"all", gocql.All},
	{"localquorum", gocql.LocalQuorum},
	{"local_quorum", gocql.LocalQuorum},
	{"eachquorum", gocql.EachQuorum},
	{"each_quorum", gocql.EachQuorum},
	{"localone", gocql.LocalOne},
	{"local_one", gocql.LocalOne},
	{" ONE ", gocql.One},
	{"Local_Quorum", gocql.LocalQuorum},
}

func TestParseConsistencyEveryName(t *testing.T) {
	for _, tt := range consistencyNameCases {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseConsistency(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
	unknown := []string{"Bogus", "  ", "local quorum", "serial"}
	for _, in := range unknown {
		t.Run("unknown "+in, func(t *testing.T) {
			got, err := parseConsistency(in)
			require.Equal(t, gocql.Quorum, got)
			require.EqualError(t, err, fmt.Sprintf("cassandra: unknown consistency %q", in))
		})
	}
}

func TestParseURLErrorsAreExact(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr string
	}{
		{"wrong scheme", "redis://h/ks", "cassandra url must start with cassandra://"},
		{"no host", "cassandra:///ks", "cassandra url must name at least one host, e.g. cassandra://host:9042/keyspace"},
		{"no host with userinfo", "cassandra://u:dummysecret@/ks", "cassandra url must name at least one host, e.g. cassandra://host:9042/keyspace"},
		{"no keyspace", "cassandra://h/", "cassandra url must name a keyspace, e.g. cassandra://host:9042/mykeyspace"},
		{"no keyspace with userinfo", "cassandra://u:dummysecret@h", "cassandra url must name a keyspace, e.g. cassandra://host:9042/mykeyspace"},
		{"two segments", "cassandra://u:dummysecret@h/a/b", "cassandra url must name a keyspace, e.g. cassandra://host:9042/mykeyspace"},
		{"unknown consistency", "cassandra://u:dummysecret@h/ks?consistency=bogus", `cassandra: unknown consistency "bogus"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseURL(tt.rawURL, "")
			require.EqualError(t, err, tt.wantErr)
			require.NotContains(t, err.Error(), "dummysecret")
		})
	}
}

func TestParseURLHostsAndKeyspaceShapes(t *testing.T) {
	t.Run("an empty host in the list stays empty", func(t *testing.T) {
		cc, err := parseURL("cassandra://a,,b/ks", "")
		require.NoError(t, err)
		require.Equal(t, []string{"a:9042", "", "b:9042"}, cc.hosts)
	})
	t.Run("slashes around the keyspace are trimmed", func(t *testing.T) {
		cc, err := parseURL("cassandra://h//ks/", "")
		require.NoError(t, err)
		require.Equal(t, "ks", cc.keyspace)
	})
	t.Run("the query is read after the path", func(t *testing.T) {
		cc, err := parseURL("cassandra://h/ks?table=t&consistency=one", "")
		require.NoError(t, err)
		require.Equal(t, "t", cc.table)
		require.Equal(t, gocql.One, cc.consistency)
	})
}

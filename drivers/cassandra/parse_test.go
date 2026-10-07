package cassandra

import (
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
	t.Run("password with at sign and colon", func(t *testing.T) {
		cc, err := parseURL("cassandra://u:ab@cd:ef@host/ks", "")
		require.NoError(t, err)
		require.Equal(t, "u", cc.username)
		require.Equal(t, "ab@cd:ef", cc.password)
		require.Equal(t, []string{"host:9042"}, cc.hosts)
	})
	t.Run("percent-encoded password", func(t *testing.T) {
		cc, err := parseURL("cassandra://u%40x:p%40ss@host/ks", "")
		require.NoError(t, err)
		require.Equal(t, "u@x", cc.username)
		require.Equal(t, "p@ss", cc.password)
		require.Equal(t, []string{"host:9042"}, cc.hosts)
	})
	t.Run("bad escape hides the password", func(t *testing.T) {
		_, err := parseURL("cassandra://u:dummysecret%zz@host/ks", "")
		require.ErrorIs(t, err, errBadUserinfo)
		require.NotContains(t, err.Error(), "dummysecret")
	})
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

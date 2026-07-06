package hbase

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseURL(t *testing.T) {
	t.Run("host table and defaults", func(t *testing.T) {
		cc, err := parseURL("hbase://zk:2181/?table=books", "")
		require.NoError(t, err)
		require.Equal(t, "zk:2181", cc.zkquorum)
		require.Equal(t, "books", cc.table)
		require.Equal(t, defaultZnode, cc.znode)
		require.Equal(t, ctAuto, cc.rowkeyType)
		require.Empty(t, cc.types)
	})

	t.Run("multi-host quorum gets default ports", func(t *testing.T) {
		cc, err := parseURL("hbase://a,b:2999,c/?table=t", "")
		require.NoError(t, err)
		require.Equal(t, "a:2181,b:2999,c:2181", cc.zkquorum)
	})

	t.Run("address overrides ?table=", func(t *testing.T) {
		cc, err := parseURL("hbase://zk/?table=default", "orders")
		require.NoError(t, err)
		require.Equal(t, "orders", cc.table)
	})

	t.Run("znode and types and rowkeytype", func(t *testing.T) {
		cc, err := parseURL("hbase://zk/?table=t&znode=/hbase-unsecure&types=cf:age=long,cf:p=double&rowkeytype=long", "")
		require.NoError(t, err)
		require.Equal(t, "/hbase-unsecure", cc.znode)
		require.Equal(t, ctLong, cc.types[cellKey("cf", "age")])
		require.Equal(t, ctDouble, cc.types[cellKey("cf", "p")])
		require.Equal(t, ctLong, cc.rowkeyType)
	})

	t.Run("namespace qualified table is allowed", func(t *testing.T) {
		cc, err := parseURL("hbase://zk/?table=ns:t", "")
		require.NoError(t, err)
		require.Equal(t, "ns:t", cc.table)
	})

	tests := []struct {
		name string
		url  string
		addr string
	}{
		{"wrong scheme", "redis://zk/", ""},
		{"no host", "hbase:///?table=t", ""},
		{"table with slash", "hbase://zk/?table=a/b", ""},
		{"bad type", "hbase://zk/?table=t&types=cf:a=blorp", ""},
		{"bad rowkeytype", "hbase://zk/?table=t&rowkeytype=blorp", ""},
		{"malformed types entry", "hbase://zk/?table=t&types=noequals", ""},
		{"types column not family:qualifier", "hbase://zk/?table=t&types=col=long", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseURL(tt.url, tt.addr)
			require.Error(t, err)
		})
	}
}

func TestWithPort(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"host", "host:2181"},
		{"host:9999", "host:9999"},
		{"[::1]", "[::1]:2181"},
		{"[::1]:2181", "[::1]:2181"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			require.Equal(t, tt.want, withPort(tt.in))
		})
	}
}

func TestParseTypeMap(t *testing.T) {
	m, err := parseTypeMap("cf:age=long, cf:price=double ,ns:x=text")
	require.NoError(t, err)
	require.Equal(t, ctLong, m[cellKey("cf", "age")])
	require.Equal(t, ctDouble, m[cellKey("cf", "price")])
	require.Equal(t, ctText, m[cellKey("ns", "x")])

	empty, err := parseTypeMap("")
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestSplitTable(t *testing.T) {
	ns, q := splitTable("books")
	require.Equal(t, defaultNamespace, ns)
	require.Equal(t, "books", q)

	ns, q = splitTable("ns:books")
	require.Equal(t, "ns", ns)
	require.Equal(t, "books", q)
}

func TestTarget(t *testing.T) {
	tbl, err := Target("hbase://zk/?table=books", "")
	require.NoError(t, err)
	require.Equal(t, "books", tbl)

	tbl, err = Target("hbase://zk/?table=books", "orders")
	require.NoError(t, err)
	require.Equal(t, "orders", tbl)

	_, err = Target("nothbase://zk/", "")
	require.Error(t, err)
}

package hbase

import "github.com/zsltg/iq/internal/query"

// The URI options that iq reads from an hbase:// URI.
const (
	paramTable   = "table"
	paramZnode   = "znode"
	paramTypes   = "types"
	paramKeytype = "keytype"
)

// URIParams lists the URI options that iq reads from an hbase:// URI.
var URIParams = []query.URIParam{
	{Name: paramTable, Desc: "default table, as [namespace:]table", Keyspace: true},
	{Name: paramZnode, Desc: "ZooKeeper znode of the cluster, /hbase by default"},
	{Name: paramTypes, Desc: "cell encodings, as family:qualifier=type pairs separated by commas"},
	{
		Name:   paramKeytype,
		Desc:   "encoding of the row key",
		Values: []string{"text", "bytes", "int", "long", "double", "bool"},
	},
}

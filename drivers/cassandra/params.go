package cassandra

import "github.com/zsltg/iq/internal/query"

// The URI options that iq reads from a cassandra:// URI.
const (
	paramTable       = "table"
	paramConsistency = "consistency"
)

// URIParams lists the URI options that iq reads from a cassandra:// URI.
var URIParams = []query.URIParam{
	{Name: paramTable, Desc: "default table", Keyspace: true},
	{
		Name: paramConsistency,
		Desc: "read consistency level, quorum by default",
		Values: []string{
			"any", "one", "two", "three", "quorum", "all",
			"local_quorum", "each_quorum", "local_one",
		},
	},
}

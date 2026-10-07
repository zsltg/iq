package elasticsearch

import "github.com/zsltg/iq/internal/query"

// paramIndex is the URI option that iq reads from an elasticsearch:// or
// opensearch:// URI.
const paramIndex = "index"

// URIParams lists the URI options that iq reads from an elasticsearch:// or
// opensearch:// URI.
var URIParams = []query.URIParam{
	{Name: paramIndex, Desc: "default index", Keyspace: true},
}

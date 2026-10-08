package mongo

import "github.com/zsltg/iq/internal/query"

// URIParams lists the URI options that iq reads from a mongodb:// URI. The
// MongoDB client reads the other options, so they are not listed here.
var URIParams = []query.URIParam{
	{Name: collectionParam, Desc: "default collection", Keyspace: true},
}

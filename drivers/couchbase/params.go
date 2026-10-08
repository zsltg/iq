package couchbase

import "github.com/zsltg/iq/internal/query"

// The URI options that iq reads from a couchbase:// URI. The Couchbase client
// reads the other options, so they are not listed here.
const (
	paramBucket     = "bucket"
	paramCollection = "collection"
)

// URIParams lists the URI options that iq reads from a couchbase:// URI.
var URIParams = []query.URIParam{
	{Name: paramCollection, Desc: "default collection, as [scope.]collection", Keyspace: true},
	{Name: paramBucket, Desc: "bucket", Keyspace: true},
}

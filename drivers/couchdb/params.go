package couchdb

import "github.com/zsltg/iq/internal/query"

// paramDatabase is the URI option that iq reads from a couchdb:// URI.
const paramDatabase = "database"

// URIParams lists the URI options that iq reads from a couchdb:// URI.
var URIParams = []query.URIParam{
	{Name: paramDatabase, Desc: "default database", Keyspace: true},
}

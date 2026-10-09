package dynamodb

import "github.com/zsltg/iq/internal/query"

// The URI options that iq reads from a dynamodb:// URI.
const (
	paramTable    = "table"
	paramEndpoint = "endpoint"
)

// URIParams lists the URI options that iq reads from a dynamodb:// URI.
var URIParams = []query.URIParam{
	{Name: paramTable, Desc: "default table", Keyspace: true},
	{Name: paramEndpoint, Desc: "endpoint override, for DynamoDB Local"},
}

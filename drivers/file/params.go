package file

import "github.com/zsltg/iq/internal/query"

// The URI options that iq reads from a file:// URI.
const (
	paramFormat  = "format"
	paramTypes   = "types"
	paramKeys    = "keys"
	paramColumns = "columns"
	paramLabel   = "label"
	paramRel     = "rel"
	paramKey     = "key"
)

// URIParams lists the URI options that iq reads from a file:// URI. None of
// them is a keyspace: label and rel select a part of a graph dump.
var URIParams = []query.URIParam{
	{
		Name: paramFormat,
		Desc: "dump format, detected from the content by default",
		Values: []string{
			"jsonl", "yaml", "mongoexport", "bson", "rdb",
			"dynamodb-json", "cassandra-csv", "neo4j-json",
		},
	},
	{Name: paramTypes, Desc: "column types of a Cassandra CSV dump, as col=cqltype pairs separated by commas"},
	{Name: paramKeys, Desc: "primary key of a DynamoDB or Cassandra dump"},
	{Name: paramColumns, Desc: "column names of a Cassandra CSV dump without a header"},
	{Name: paramLabel, Desc: "node label to read from a Neo4j dump"},
	{Name: paramRel, Desc: "relationship type to read from a Neo4j dump"},
	{Name: paramKey, Desc: "property that keys the records of a Neo4j dump"},
}

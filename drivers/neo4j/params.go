package neo4j

import "github.com/zsltg/iq/internal/query"

// The URI options that iq reads from a neo4j:// or bolt:// URI.
const (
	paramLabel    = "label"
	paramRel      = "rel"
	paramDatabase = "database"
	paramKey      = "key"
)

// URIParams lists the URI options that iq reads from a neo4j:// or bolt:// URI.
var URIParams = []query.URIParam{
	{Name: paramLabel, Desc: "default node label", Keyspace: true},
	{Name: paramRel, Desc: "default relationship type, instead of a label", Keyspace: true},
	{Name: paramDatabase, Desc: "database, neo4j by default", Keyspace: true},
	{Name: paramKey, Desc: "property that keys the records, the element id by default"},
}

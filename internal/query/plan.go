package query

// AccessPlan describes the backend calls a source will make for one query. A CLI
// adapter uses it to show a query plan without executing the query. It is a plain type
// that crosses the driver boundary: a driver builds one from the route classification
// and the pushed predicate, and the CLI renders it. Ops holds the human-readable
// planned operations in execution order. Filter is the planned scan filter as a
// JSON-ready value: a server-side filter (a MongoDB find filter) or a client-side
// prefilter (the Redis raw-byte predicate). It is nil when no filter is planned.
type AccessPlan struct {
	Ops    []string
	Filter map[string]any
}

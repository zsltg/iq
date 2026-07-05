package query

// AccessPlan describes the backend calls a source will make for one query, the
// data a CLI adapter needs to show a query plan without executing it. It is a
// plain type crossing the driver boundary: a driver builds one from the route
// classification and the pushed predicate, and the CLI renders it. Ops holds the
// human-readable planned operations in execution order; Filter is the server-side
// filter as a JSON-ready value (a MongoDB find filter), or nil when the backend
// filters nothing server-side (Redis, or a full scan).
type AccessPlan struct {
	Ops    []string
	Filter map[string]any
}

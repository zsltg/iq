package query

// URIParam describes one URI query option that iq itself reads, such as
// ?table= or ?consistency=. Each driver package exports a catalogue of these,
// and its URI parse code uses the same names. The CLI derives the keyspace
// params of its registry and the long help of `iq add` from the catalogues.
// Options that a backend SDK parses are not listed.
type URIParam struct {
	// Name is the query key, without the "?" and the "=".
	Name string
	// Desc is a short description for the help text.
	Desc string
	// Values is the closed set of accepted values. It is nil for a free value.
	Values []string
	// Keyspace marks an option that pins the default keyspace of a source, such
	// as a collection or a table. The order of a catalogue is the order of
	// precedence: an earlier keyspace option is more specific.
	Keyspace bool
}

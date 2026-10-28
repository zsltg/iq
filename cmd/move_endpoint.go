package cmd

import (
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// pathUse says what the path of a connection URI selects for a backend.
type pathUse int

const (
	// The zero value means the backend does not read the path. A driver entry
	// gets it by leaving the path field out.
	_ pathUse = iota
	// pathScope means the path names the scope that holds the address: the
	// database of MongoDB or the keyspace of Cassandra.
	pathScope
	// pathAddress means the path names the address when no other source does:
	// the database of CouchDB or the index of Elasticsearch.
	pathAddress
	// pathDBIndex means the path is the numeric database of Redis. An empty
	// path is database 0.
	pathDBIndex
)

// endpointShape says how a URI of one backend names its server and container.
// The zero value means no default port, a port that counts, and a path that
// the backend ignores.
type endpointShape struct {
	// defaultPort is the port that a host without a port falls back to.
	defaultPort string
	// anyPort makes the replace guard ignore ports. Couchbase bootstrap ports
	// vary with TLS and with the service, but they reach the same cluster.
	anyPort bool
	path    pathUse
}

// containerKey is the identity of the container that an endpoint reads and
// writes. Two endpoints with the same key name the same container, even when
// their URL strings differ.
type containerKey struct {
	driver string
	// hosts is the set of servers, as host:port. A seed list has many.
	hosts []string
	// rest joins the scope, the address, and the broad keyspace option.
	rest string
}

// sameContainer reports whether the canonical keys of a and b match. Two
// endpoints match when they use one driver family, share at least one host,
// and have the same container below the host. It returns false when either
// endpoint has no key.
func sameContainer(a, b endpoint) bool {
	ka, ok := endpointContainerKey(a)
	if !ok {
		return false
	}
	kb, ok := endpointContainerKey(b)
	if !ok {
		return false
	}
	if ka.driver != kb.driver || ka.rest != kb.rest {
		return false
	}
	for _, h := range ka.hosts {
		if slices.Contains(kb.hosts, h) {
			return true
		}
	}
	return false
}

// endpointContainerKey builds the canonical key of e from its URL. It reports
// false when the endpoint has no URL, uses a read-only or unknown driver, or
// has a URL that does not parse. The caller then keeps the string comparison.
// The user info and the options that do not select a container take no part.
func endpointContainerKey(e endpoint) (containerKey, bool) {
	if e.url == "" {
		return containerKey{}, false
	}
	scheme := schemeOf(e.url)
	d, ok := driverForScheme(scheme)
	if !ok || d.readOnly {
		return containerKey{}, false
	}
	rest, rawQuery, _ := strings.Cut(strings.SplitN(e.url, "://", 2)[1], "?")
	rawQuery, _, _ = strings.Cut(rawQuery, "#")
	rest, _, _ = strings.Cut(rest, "#")
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return containerKey{}, false
	}
	authority, path, _ := strings.Cut(rest, "/")
	path = strings.Trim(path, "/")
	hosts := canonicalHosts(authority, d.shape, !strings.HasSuffix(scheme, "+srv"))
	if len(hosts) == 0 {
		return containerKey{}, false
	}

	// The address is read from the parsed query, because urlAddressName cannot
	// parse a URL with a host list.
	params := d.addressParams()
	address := e.address
	for _, p := range params {
		if address == "" {
			address = q.Get(p)
		}
	}
	var scope string
	switch d.shape.path {
	case pathScope:
		scope = path
	case pathAddress:
		if address == "" {
			address = path
		}
	case pathDBIndex:
		scope = redisDBIndex(path, q.Get("db"))
	}
	if d.name == "couchbase" {
		address = couchbaseCollection(address)
	}
	broad := ""
	if len(params) > 1 {
		broad = q.Get(params[len(params)-1])
	}
	name := d.name
	if name == "opensearch" {
		// OpenSearch shares the Elasticsearch adapter, so one endpoint can
		// name the same index through either scheme.
		name = "elasticsearch"
	}
	return containerKey{
		driver: name,
		hosts:  hosts,
		rest:   strings.Join([]string{scope, address, broad}, "\x00"),
	}, true
}

// canonicalHosts parses the authority of a URI into host:port items. It drops
// the user info, lowercases each host, maps every loopback address to one
// name, and adds the default port when fillPort is set and a host has none.
func canonicalHosts(authority string, shape endpointShape, fillPort bool) []string {
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		authority = authority[i+1:]
	}
	var hosts []string
	for h := range strings.SplitSeq(authority, ",") {
		host, port, err := net.SplitHostPort(h)
		if err != nil {
			host, port = strings.Trim(h, "[]"), ""
		}
		host = strings.ToLower(host)
		if host == "" {
			continue
		}
		if host == "localhost" || net.ParseIP(host).IsLoopback() {
			host = "localhost"
		}
		if shape.anyPort {
			port = ""
		} else if port == "" && fillPort {
			port = shape.defaultPort
		}
		hosts = append(hosts, host+":"+port)
	}
	return hosts
}

// redisDBIndex returns the Redis database that a URI selects: the path, else
// the db option, else 0. A number has no leading zeros.
func redisDBIndex(path, option string) string {
	db := path
	if db == "" {
		db = option
	}
	if db == "" {
		return "0"
	}
	if n, err := strconv.Atoi(db); err == nil {
		return strconv.Itoa(n)
	}
	return db
}

// couchbaseCollection returns the [scope.]collection address in its full form.
// An empty address and a bare name use the default scope.
func couchbaseCollection(address string) string {
	switch {
	case address == "":
		return "_default._default"
	case !strings.Contains(address, "."):
		return "_default." + address
	}
	return address
}

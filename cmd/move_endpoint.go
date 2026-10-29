package cmd

import (
	"net"
	"net/netip"
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
	// tlsPort is the fallback port for the schemes in tlsSchemes. An empty
	// value means they use defaultPort.
	tlsPort    string
	tlsSchemes []string
	// anyPort makes the replace guard ignore ports. Couchbase bootstrap ports
	// vary with TLS and with the service, but they reach the same cluster.
	anyPort bool
	path    pathUse
}

// portFor returns the port that a host without a port uses under scheme. It
// returns the empty string when the scheme gives no port, as an SRV scheme
// does.
func (s endpointShape) portFor(scheme string) string {
	if strings.HasSuffix(scheme, "+srv") {
		return ""
	}
	if s.tlsPort != "" && slices.Contains(s.tlsSchemes, scheme) {
		return s.tlsPort
	}
	return s.defaultPort
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
	d, ok := writableDriverOf(e.url)
	if !ok {
		return containerKey{}, false
	}
	authority, path, q, ok := splitEndpointURL(e.url)
	if !ok {
		return containerKey{}, false
	}
	hosts := canonicalHosts(authority, d.shape, d.shape.portFor(schemeOf(e.url)))
	if len(hosts) == 0 {
		return containerKey{}, false
	}
	scope, address, broad := containerParts(d, e.address, path, q)
	return containerKey{
		driver: keyDriverName(d.name),
		hosts:  hosts,
		rest:   strings.Join([]string{scope, address, broad}, "\x00"),
	}, true
}

// writableDriverOf returns the driver of a URL. It reports false when the URL
// is empty, has an unknown scheme, or names a read-only driver.
func writableDriverOf(rawURL string) (driver, bool) {
	if rawURL == "" {
		return driver{}, false
	}
	d, ok := driverForScheme(schemeOf(rawURL))
	if !ok || d.readOnly {
		return driver{}, false
	}
	return d, true
}

// splitEndpointURL cuts a connection URI into its authority, its path without
// outer slashes, and its parsed query. The fragment is dropped. It reports
// false when the query does not parse. A URL with a host list cannot go through
// net/url, so the cut is by hand.
func splitEndpointURL(rawURL string) (authority, path string, q url.Values, ok bool) {
	rest, rawQuery, _ := strings.Cut(strings.SplitN(rawURL, "://", 2)[1], "?")
	rawQuery, _, _ = strings.Cut(rawQuery, "#")
	rest, _, _ = strings.Cut(rest, "#")
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", "", nil, false
	}
	authority, path, _ = strings.Cut(rest, "/")
	return authority, strings.Trim(path, "/"), q, true
}

// containerParts returns the scope, the address, and the broad keyspace option
// that name the container below the host. The address is read from the parsed
// query, because urlAddressName cannot parse a URL with a host list.
func containerParts(d driver, explicit, path string, q url.Values) (scope, address, broad string) {
	params := d.addressParams()
	address = addressFromQuery(explicit, params, q)
	if d.shape.path == pathAddress && address == "" {
		address = path
	}
	if d.name == "couchbase" {
		address = couchbaseCollection(address)
	}
	if len(params) > 1 {
		broad = q.Get(params[len(params)-1])
	}
	return containerScope(d.shape.path, path, q), address, broad
}

// addressFromQuery returns the explicit address, else the first of params that
// the query sets, else the empty string.
func addressFromQuery(explicit string, params []string, q url.Values) string {
	if explicit != "" {
		return explicit
	}
	for _, p := range params {
		if v := q.Get(p); v != "" {
			return v
		}
	}
	return ""
}

// containerScope returns the part of the container that the path or an option
// names above the address. Most backends have none.
func containerScope(use pathUse, path string, q url.Values) string {
	switch use {
	case pathScope:
		return path
	case pathDBIndex:
		return redisDBIndex(path, q.Get("db"))
	}
	return ""
}

// keyDriverName returns the driver family of name. OpenSearch shares the
// Elasticsearch adapter, so one endpoint can name the same index through
// either scheme.
func keyDriverName(name string) string {
	if name == "opensearch" {
		return "elasticsearch"
	}
	return name
}

// canonicalHosts parses the authority of a URI into host:port items. It drops
// the user info and gives each host one spelling. See canonicalHost. A host
// without a port gets fallbackPort.
func canonicalHosts(authority string, shape endpointShape, fallbackPort string) []string {
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		authority = authority[i+1:]
	}
	var hosts []string
	for h := range strings.SplitSeq(authority, ",") {
		if host, ok := canonicalHost(h, shape, fallbackPort); ok {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// canonicalHost turns one host item into host:port. It reports false when the
// host name is empty. See canonicalHostName for the spelling of the host.
func canonicalHost(item string, shape endpointShape, fallbackPort string) (string, bool) {
	host, port, err := net.SplitHostPort(item)
	if err != nil {
		host, port = strings.Trim(item, "[]"), ""
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return "", false
	}
	return canonicalHostName(host) + ":" + canonicalPort(port, shape, fallbackPort), true
}

// canonicalHostName gives one text to each spelling of a server. An IP literal
// has its shortest form, and an IPv4-mapped IPv6 address becomes its IPv4
// address. Every loopback address becomes localhost.
func canonicalHostName(host string) string {
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if addr.IsLoopback() {
			return "localhost"
		}
		return addr.String()
	}
	return host
}

// canonicalPort returns the port that counts for the replace guard.
func canonicalPort(port string, shape endpointShape, fallbackPort string) string {
	if shape.anyPort {
		return ""
	}
	if port == "" {
		return fallbackPort
	}
	return port
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

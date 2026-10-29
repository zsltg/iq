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

// port returns the port of host h in the replace guard key. It is the empty
// string when the shape ignores ports. A host without a port gets the default
// port of the scheme of u.
func (s endpointShape) port(h hostSpec, u endpointURL) string {
	if s.anyPort {
		return ""
	}
	if h.port != "" {
		return h.port
	}
	return s.defaultPortFor(u)
}

// defaultPortFor returns the port that a host without a port uses under the
// scheme of u. It returns the empty string when the scheme gives no port, as
// an SRV scheme does.
func (s endpointShape) defaultPortFor(u endpointURL) string {
	if strings.HasSuffix(u.scheme, "+srv") {
		return ""
	}
	if s.tlsPort != "" && slices.Contains(s.tlsSchemes, u.scheme) {
		return s.tlsPort
	}
	return s.defaultPort
}

// scope returns the part of the container that the path or an option of u
// names above the address. Most backends have none.
func (s endpointShape) scope(u endpointURL) string {
	switch s.path {
	case pathScope:
		return u.path
	case pathDBIndex:
		return u.redisDB()
	}
	return ""
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
	d, ok := writableDriverOf(e)
	if !ok {
		return containerKey{}, false
	}
	u, ok := parseEndpointURL(e)
	if !ok {
		return containerKey{}, false
	}
	hosts := u.hosts(d.shape)
	if len(hosts) == 0 {
		return containerKey{}, false
	}
	return containerKey{
		driver: d.family(),
		hosts:  hosts,
		rest:   u.rest(d),
	}, true
}

// writableDriverOf returns the driver of the URL of e. It reports false when
// the URL is empty, has an unknown scheme, or names a read-only driver.
func writableDriverOf(e endpoint) (driver, bool) {
	if e.url == "" {
		return driver{}, false
	}
	d, ok := driverForScheme(schemeOf(e.url))
	if !ok || d.readOnly {
		return driver{}, false
	}
	return d, true
}

// family returns the driver family of d. OpenSearch shares the Elasticsearch
// adapter, so one endpoint can name the same index through either scheme.
func (d driver) family() string {
	if d.name == "opensearch" {
		return "elasticsearch"
	}
	return d.name
}

// endpointURL holds the parts of an endpoint that name a container.
type endpointURL struct {
	scheme    string
	authority string
	// path has no outer slashes.
	path  string
	query url.Values
	// address is the explicit address of the endpoint. It wins over the query.
	address string
}

// parseEndpointURL cuts the URL of e into its scheme, its authority, its path
// without outer slashes, and its parsed query. The fragment is dropped. It
// reports false when the query does not parse. A URL with a host list cannot
// go through net/url, so the cut is by hand. The caller makes sure that the
// URL has a scheme.
func parseEndpointURL(e endpoint) (endpointURL, bool) {
	rest, rawQuery, _ := strings.Cut(strings.SplitN(e.url, "://", 2)[1], "?")
	rawQuery, _, _ = strings.Cut(rawQuery, "#")
	rest, _, _ = strings.Cut(rest, "#")
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return endpointURL{}, false
	}
	authority, path, _ := strings.Cut(rest, "/")
	return endpointURL{
		scheme:    schemeOf(e.url),
		authority: authority,
		path:      strings.Trim(path, "/"),
		query:     q,
		address:   e.address,
	}, true
}

// rest joins the scope, the address, and the broad keyspace option that name
// the container below the host. The address is read from the parsed query,
// because urlAddressName cannot parse a URL with a host list.
func (u endpointURL) rest(d driver) string {
	params := d.addressParams()
	address := u.addressFrom(params)
	if d.shape.path == pathAddress && address == "" {
		address = u.path
	}
	if d.name == "couchbase" {
		address = couchbaseCollection(address)
	}
	broad := ""
	if len(params) > 1 {
		broad = u.query.Get(params[len(params)-1])
	}
	return strings.Join([]string{d.shape.scope(u), address, broad}, "\x00")
}

// addressFrom returns the explicit address, else the first of params that the
// query sets, else the empty string.
func (u endpointURL) addressFrom(params []string) string {
	if u.address != "" {
		return u.address
	}
	for _, p := range params {
		if v := u.query.Get(p); v != "" {
			return v
		}
	}
	return ""
}

// redisDB returns the Redis database that u selects: the path, else the db
// option, else 0. A number has no leading zeros.
func (u endpointURL) redisDB() string {
	db := u.path
	if db == "" {
		db = u.query.Get("db")
	}
	if db == "" {
		return "0"
	}
	if n, err := strconv.Atoi(db); err == nil {
		return strconv.Itoa(n)
	}
	return db
}

// hosts parses the authority of u into host:port items. It drops the user info
// and gives each host one spelling. See hostSpec.canonical. It skips an item
// with an empty host name.
func (u endpointURL) hosts(shape endpointShape) []string {
	authority := u.authority
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		authority = authority[i+1:]
	}
	var hosts []string
	for item := range strings.SplitSeq(authority, ",") {
		h := newHostSpec(item).canonical()
		if h.name == "" {
			continue
		}
		h.port = shape.port(h, u)
		hosts = append(hosts, h.name+":"+h.port)
	}
	return hosts
}

// hostSpec is one host item of an authority.
type hostSpec struct {
	name string
	// port is empty when the item has none.
	port string
}

// newHostSpec splits one host item into its name and its port.
func newHostSpec(item string) hostSpec {
	name, port, err := net.SplitHostPort(item)
	if err != nil {
		return hostSpec{name: strings.Trim(item, "[]")}
	}
	return hostSpec{name: name, port: port}
}

// canonical gives one text to each spelling of a server. The name is lower
// case and has no trailing dot. An IP literal has its shortest form, and an
// IPv4-mapped IPv6 address becomes its IPv4 address. Every loopback address
// becomes localhost. An empty name stays empty.
func (h hostSpec) canonical() hostSpec {
	h.name = strings.TrimSuffix(strings.ToLower(h.name), ".")
	if addr, err := netip.ParseAddr(h.name); err == nil {
		addr = addr.Unmap()
		if addr.IsLoopback() {
			h.name = "localhost"
		} else {
			h.name = addr.String()
		}
	}
	return h
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

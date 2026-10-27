package hbase

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// defaultZKPort is the ZooKeeper client port a quorum host without an explicit port
// falls back to.
const defaultZKPort = "2181"

// defaultZnode is the ZooKeeper znode parent HBase registers under by default.
const defaultZnode = "/hbase"

// connConfig is the parsed form of an hbase:// source URL.
type connConfig struct {
	// zkquorum is the comma-separated host:port list gohbase dials ZooKeeper on.
	zkquorum string
	// znode is the ZooKeeper parent znode HBase registers under.
	znode string
	// table is the [namespace:]table that the jq and write paths are scoped to. It
	// is empty for raw-only or list-only use.
	table string
	// types declares the encoding of specific columns, keyed "family:qualifier".
	types typeMap
	// rowkeyType is the declared encoding of the row key (ctAuto when undeclared).
	rowkeyType colType
}

// errEmptyHost and errUserinfoInHosts are fixed errors. They never quote the
// input, because the authority of a hand-written URI can hold a password.
var (
	errEmptyHost       = errors.New("hbase url has an empty host in its ZooKeeper host list")
	errUserinfoInHosts = errors.New("hbase url does not accept user info in its ZooKeeper host list")
)

// parseURL parses an hbase:// source URL into its connection config. It is
// hand-rolled for the authority (a multi-host ZooKeeper quorum, hbase://h1,h2/, is
// not a valid net/url host), then uses net/url only for the query string. The
// address override, when non-empty, wins over the URL's ?table= default. No path is
// required: the host is the quorum and the table is a query/address concern.
func parseURL(rawURL, address string) (connConfig, error) {
	const scheme = "hbase://"
	if !strings.HasPrefix(rawURL, scheme) {
		return connConfig{}, fmt.Errorf("hbase url must start with %s", scheme)
	}
	rest := rawURL[len(scheme):]

	rest, rawQuery, _ := strings.Cut(rest, "?")
	authority, _, _ := strings.Cut(rest, "/")
	if authority == "" {
		return connConfig{}, fmt.Errorf("hbase url must name at least one ZooKeeper host, e.g. hbase://host:2181/?table=books")
	}
	// The ZooKeeper host list has no user info. A "@" here can hide a password,
	// so the errors below never quote the authority.
	if strings.Contains(authority, "@") {
		return connConfig{}, errUserinfoInHosts
	}
	hosts := strings.Split(authority, ",")
	for i, h := range hosts {
		if h == "" {
			return connConfig{}, errEmptyHost
		}
		hosts[i] = withPort(h)
	}

	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return connConfig{}, fmt.Errorf("parse hbase url query: %w", err)
	}

	table := address
	if table == "" {
		table = q.Get(paramTable)
	}
	if strings.Contains(table, "/") {
		return connConfig{}, fmt.Errorf("hbase table %q must be [namespace:]table, not a path", table)
	}

	znode := q.Get(paramZnode)
	if znode == "" {
		znode = defaultZnode
	}

	types, err := parseTypeMap(q.Get(paramTypes))
	if err != nil {
		return connConfig{}, err
	}
	rowkeyType := ctAuto
	if rk := q.Get(paramKeytype); rk != "" {
		rowkeyType, err = parseColType(rk)
		if err != nil {
			return connConfig{}, err
		}
	}

	return connConfig{
		zkquorum:   strings.Join(hosts, ","),
		znode:      znode,
		table:      table,
		types:      types,
		rowkeyType: rowkeyType,
	}, nil
}

// withPort appends the default ZooKeeper port to a bare host. It leaves untouched
// a host that already carries a port (or a bracketed IPv6 literal with one).
func withPort(host string) string {
	if strings.HasPrefix(host, "[") {
		if strings.Contains(host, "]:") {
			return host
		}
		return host + ":" + defaultZKPort
	}
	if strings.Contains(host, ":") {
		return host
	}
	return host + ":" + defaultZKPort
}

// parseTypeMap parses the ?types= parameter into a typeMap. The parameter is
// a comma-separated list of family:qualifier=type entries, for example
// "cf:age=long,cf:price=double". An empty parameter yields an empty map
// (every column ctAuto). A malformed entry or unknown type is an error,
// failing fast at connect.
func parseTypeMap(s string) (typeMap, error) {
	out := typeMap{}
	if s == "" {
		return out, nil
	}
	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		col, typeName, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("hbase: malformed ?types= entry %q; want family:qualifier=type", entry)
		}
		family, qualifier, ok := strings.Cut(col, ":")
		if !ok || family == "" || qualifier == "" {
			return nil, fmt.Errorf("hbase: ?types= column %q must be family:qualifier", col)
		}
		ct, err := parseColType(strings.TrimSpace(typeName))
		if err != nil {
			return nil, err
		}
		out[cellKey(family, qualifier)] = ct
	}
	return out, nil
}

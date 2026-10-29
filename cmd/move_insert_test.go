package cmd

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// mvLog records what the fake move stores see, in order.
type mvLog struct {
	mu       sync.Mutex
	events   []string
	ctxs     map[string]context.Context
	modes    []query.WriteMode
	batches  [][]query.Record
	clearErr error
	putErr   error
	closed   int
}

func (l *mvLog) event(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *mvLog) seen(ctx context.Context, where string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ctxs[where] = ctx
}

// mvBare is a store with no write and no typed read port.
type mvBare struct {
	fakeInspectStore
	log *mvLog
}

func (s mvBare) Close() error {
	s.log.mu.Lock()
	defer s.log.mu.Unlock()
	s.log.closed++
	return nil
}

// mvPut is a store that can be written but not cleared.
type mvPut struct {
	mvBare
	url string
}

func (s mvPut) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	s.log.event("put")
	s.log.seen(ctx, "put")
	s.log.mu.Lock()
	defer s.log.mu.Unlock()
	s.log.modes = append(s.log.modes, mode)
	s.log.batches = append(s.log.batches, batch)
	if s.log.putErr != nil {
		return query.WriteStat{}, fmt.Errorf("write to %s: %w", s.url, s.log.putErr)
	}
	return query.WriteStat{Written: len(batch)}, nil
}

// mvFull is a store that can be read, written and cleared.
type mvFull struct{ mvPut }

func (s mvFull) Clear(ctx context.Context) error {
	s.log.event("clear")
	s.log.seen(ctx, "clear")
	if s.log.clearErr != nil {
		return fmt.Errorf("clear %s: %w", s.url, s.log.clearErr)
	}
	return nil
}

func (s mvFull) TypedScan(ctx context.Context, fn func([]query.Record) error) error {
	s.log.seen(ctx, "scan")
	return fn([]query.Record{{Key: "k", Type: "string", Value: "v"}})
}

// useMoveDrivers registers the mvfull, mvput and mvbare schemes and seeds one
// source of each under the handles full, put and bare. The full source URI
// carries a password so that a redaction check has something to hide.
func useMoveDrivers(t *testing.T) *mvLog {
	t.Helper()
	log := &mvLog{ctxs: map[string]context.Context{}}
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	mk := func(scheme string, build func(url string) store) driver {
		return driver{
			name: scheme, schemes: []string{scheme},
			open: func(ctx context.Context, cfg *config) (store, error) {
				log.seen(ctx, "open:"+scheme)
				return build(cfg.url), nil
			},
		}
	}
	mvfull := mk("mvfull", func(u string) store { return mvFull{mvPut{mvBare{log: log}, u}} })
	mvfull.shape = endpointShape{defaultPort: "9999"}
	drivers = append(append([]driver{}, orig...),
		mvfull,
		mk("mvput", func(u string) store { return mvPut{mvBare{log: log}, u} }),
		mk("mvbare", func(string) store { return mvBare{log: log} }),
		driver{
			name: "mvfail", schemes: []string{"mvfail"},
			open: func(_ context.Context, cfg *config) (store, error) {
				return nil, fmt.Errorf("dial %s: refused", cfg.url)
			},
		},
	)
	c := newSeed()
	require.NoError(t, c.Add("full", "mvfull://u:hunter2@h"))
	require.NoError(t, c.Add("alias", "mvfull://H:9999"))
	require.NoError(t, c.Add("other", "mvfull://elsewhere"))
	require.NoError(t, c.Add("put", "mvput://h"))
	require.NoError(t, c.Add("bare", "mvbare://h"))
	require.NoError(t, c.Add("down", "mvfail://u:hunter2@h"))
	seedConfig(t, c)
	return log
}

// oneBatch is a record source that offers one batch of two records.
func oneBatch(_ context.Context, fn func([]query.Record) error) error {
	return fn([]query.Record{{Key: "a", Type: "string", Value: "1"}, {Key: "b", Type: "string", Value: "2"}})
}

func TestApplyInsertOrder(t *testing.T) {
	tests := []struct {
		name string
		prep func(t *testing.T)
		req  insertRequest
		want string
	}{
		{"a corrupt config", func(t *testing.T) { corruptConfig(t, "") }, insertRequest{dst: "full"}, "parse config"},
		{"an unknown destination", nil, insertRequest{dst: "nosuch"}, "nosuch"},
		{"a saved dump file", func(t *testing.T) { seedFileSource(t, moveDump) }, insertRequest{dst: "snap"}, "destination snap (file) cannot be written to"},
		{"a store with no Put", nil, insertRequest{dst: "bare"}, "destination bare (mvbare) cannot be written to"},
		{"replace on a store with no Clear", nil, insertRequest{dst: "put", replace: true}, "--replace: destination put (mvput) cannot be cleared"},
		{"replace on a dry run still needs Clear", nil, insertRequest{dst: "put", replace: true, dryRun: true}, "--replace: destination put (mvput) cannot be cleared"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := useMoveDrivers(t)
			if tt.prep != nil {
				tt.prep(t)
			}
			tt.req.confirm = func(string) error { t.Fatal("confirm must not run"); return nil }
			_, _, err := applyInsert(context.Background(), tt.req, oneBatch, nil)
			require.ErrorContains(t, err, tt.want)
			require.Empty(t, log.events, "nothing is cleared or written")
		})
	}
}

func TestApplyInsertReplaceFlow(t *testing.T) {
	run := func(t *testing.T, log *mvLog, req insertRequest) (string, query.WriteStat, error) {
		t.Helper()
		req.dst = "full"
		req.replace = true
		if req.confirm == nil {
			req.confirm = func(action string) error { log.event("confirm:" + action); return nil }
		}
		return applyInsert(context.Background(), req, oneBatch, nil)
	}

	t.Run("confirm, then clear, then put", func(t *testing.T) {
		log := useMoveDrivers(t)
		label, stat, err := run(t, log, insertRequest{mode: query.Upsert})
		require.NoError(t, err)
		require.Equal(t, "full", label)
		require.Equal(t, 2, stat.Written)
		require.Equal(t, []string{"confirm:clear full before writing", "clear", "put"}, log.events)
		require.Equal(t, 1, log.closed, "the store is closed once")
	})

	t.Run("a confirm error stops before Clear and Put", func(t *testing.T) {
		log := useMoveDrivers(t)
		refuse := errors.New("no thanks")
		_, _, err := run(t, log, insertRequest{confirm: func(string) error { return refuse }})
		require.Same(t, refuse, err)
		require.Empty(t, log.events)
		require.Equal(t, 1, log.closed)
	})

	t.Run("a Clear error is redacted", func(t *testing.T) {
		log := useMoveDrivers(t)
		log.clearErr = errors.New("boom")
		_, _, err := run(t, log, insertRequest{})
		require.ErrorContains(t, err, "mvfull://u:xxxxx@h")
		require.NotContains(t, err.Error(), "hunter2")
		require.NotContains(t, log.events, "put")
	})

	t.Run("a Put error is redacted", func(t *testing.T) {
		log := useMoveDrivers(t)
		log.putErr = errors.New("boom")
		_, _, err := run(t, log, insertRequest{})
		require.ErrorContains(t, err, "mvfull://u:xxxxx@h")
		require.NotContains(t, err.Error(), "hunter2")
	})

	t.Run("a dry run neither asks, clears nor writes", func(t *testing.T) {
		log := useMoveDrivers(t)
		_, stat, err := run(t, log, insertRequest{dryRun: true})
		require.NoError(t, err)
		require.Empty(t, log.events)
		require.Equal(t, 2, stat.Written, "the plan still counts the records")
	})
}

type mvCtxKey struct{}

func TestApplyInsertReachesTheStore(t *testing.T) {
	for _, mode := range []query.WriteMode{query.InsertOnly, query.Upsert} {
		t.Run(fmt.Sprint("mode ", mode), func(t *testing.T) {
			log := useMoveDrivers(t)
			ctx := context.WithValue(context.Background(), mvCtxKey{}, "marker")
			upper := func(r query.Record) ([]query.Record, error) {
				r.Value = "T" + r.Value.(string)
				return []query.Record{r}, nil
			}
			req := insertRequest{dst: "full", mode: mode, replace: true, confirm: func(string) error { return nil }}
			_, _, err := applyInsert(ctx, req, oneBatch, upper)
			require.NoError(t, err)
			require.Equal(t, []query.WriteMode{mode}, log.modes)
			require.Equal(t, "T1", log.batches[0][0].Value)
			for _, where := range []string{"open:mvfull", "clear", "put"} {
				require.Equal(t, "marker", log.ctxs[where].Value(mvCtxKey{}), where)
			}
		})
	}
}

func TestSameEndpoint(t *testing.T) {
	tests := []struct {
		name string
		a, b endpoint
		want bool
	}{
		{"same handle", endpoint{handle: "x", url: "redis://h/0"}, endpoint{handle: "x", url: "redis://h/0"}, true},
		{"same handle with another URL", endpoint{handle: "x", url: "redis://h/0"}, endpoint{handle: "x", url: "redis://other/0"}, true},
		{"two handles with the same URL", endpoint{handle: "x", url: "redis://h/0"}, endpoint{handle: "y", url: "redis://h/0"}, true},
		{"same handle and address", endpoint{handle: "m", url: "mongodb://h/db", address: "orders"}, endpoint{handle: "m", url: "mongodb://h/db", address: "orders"}, true},
		{"same URL with another address", endpoint{handle: "m", url: "mongodb://h/db", address: "orders"}, endpoint{handle: "n", url: "mongodb://h/db", address: "items"}, false},
		{"same handle with another address", endpoint{handle: "m", url: "mongodb://h/db", address: "orders"}, endpoint{handle: "m", url: "mongodb://h/db", address: "items"}, false},
		{"different URLs", endpoint{handle: "x", url: "redis://h/0"}, endpoint{handle: "y", url: "redis://h/1"}, false},
		{"empty source", endpoint{}, endpoint{handle: "x", url: "redis://h/0"}, false},
		{"two empty endpoints", endpoint{}, endpoint{}, false},
		{"no handles and different URLs", endpoint{url: "redis://h/0"}, endpoint{url: "redis://h/1"}, false},
		{"no URLs and different handles", endpoint{handle: "x"}, endpoint{handle: "y"}, false},
		{"no handles and the same URL", endpoint{url: "redis://h/0"}, endpoint{url: "redis://h/0"}, true},
		{"URL default address and the same explicit address", endpoint{handle: "m", url: "mongodb://h/db?collection=books"}, endpoint{handle: "m", url: "mongodb://h/db?collection=books", address: "books"}, true},
		{"URL default address and another explicit address", endpoint{handle: "m", url: "mongodb://h/db?collection=books"}, endpoint{handle: "m", url: "mongodb://h/db?collection=books", address: "orders"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, sameEndpoint(tc.a, tc.b))
		})
	}
}

func TestSameContainer(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"redis bare host and explicit port", "redis://prod.internal", "redis://prod.internal:6379", true},
		{"redis bare host and DB 0", "redis://prod.internal", "redis://prod.internal/0", true},
		{"redis bare host and port with DB 0", "redis://prod.internal", "redis://prod.internal:6379/0", true},
		{"redis port and port with DB 0", "redis://prod.internal:6379", "redis://prod.internal:6379/0", true},
		{"redis DB 0 and port with DB 0", "redis://prod.internal/0", "redis://prod.internal:6379/0", true},
		{"redis port and DB 0", "redis://prod.internal:6379", "redis://prod.internal/0", true},
		{"redis DB 0 and DB 1", "redis://prod.internal/0", "redis://prod.internal/1", false},
		{"redis DB 1 as option and as path", "redis://h?db=1", "redis://h:6379/1", true},
		{"redis TLS scheme", "rediss://h", "redis://h:6379/0", true},
		{"redis host case", "redis://PROD.internal", "redis://prod.internal", true},
		{"redis user info", "redis://u:pw@h/0", "redis://other:secret@h:6379", true},
		{"redis timeout option", "redis://h?dial_timeout=1s", "redis://h", true},
		{"redis other port", "redis://h:6380", "redis://h:6379", false},
		{"different hosts", "redis://h1", "redis://h2", false},
		{"loopback names", "redis://localhost", "redis://127.0.0.1:6379", true},
		{"loopback IPv6", "redis://[::1]", "redis://localhost:6379", true},
		{"mongo default and explicit port", "mongodb://localhost/db?collection=orders", "mongodb://localhost:27017/db?collection=orders", true},
		{"mongo loopback and port", "mongodb://127.0.0.1/db?collection=orders", "mongodb://localhost:27017/db?collection=orders", true},
		{"mongo other collection", "mongodb://h/db?collection=orders", "mongodb://h/db?collection=items", false},
		{"mongo other database", "mongodb://h/db1?collection=orders", "mongodb://h/db2?collection=orders", false},
		{"mongo seed lists with one common host", "mongodb://a,b/db?collection=c", "mongodb://b:27017,c/db?collection=c", true},
		{"mongo seed lists with no common host", "mongodb://a,b/db?collection=c", "mongodb://c,d/db?collection=c", false},
		{"mongo SRV and plain with the same host", "mongodb+srv://h/db?collection=c", "mongodb://h/db?collection=c", false},
		{"mongo SRV twice", "mongodb+srv://u:p@h/db?collection=c", "mongodb+srv://H/db?collection=c", true},
		{"cassandra host lists with one common host", "cassandra://a,b/ks?table=t", "cassandra://b:9042,c/ks?table=t", true},
		{"cassandra other keyspace", "cassandra://a/ks1?table=t", "cassandra://a/ks2?table=t", false},
		{"elasticsearch and opensearch", "elasticsearch://h:9200/?index=i", "opensearch://h:9200/?index=i", true},
		{"elasticsearch TLS scheme", "elasticsearch+s://h:9200/?index=i", "elasticsearch://h:9200/?index=i", true},
		{"elasticsearch index in the path", "elasticsearch://h:9200/i", "elasticsearch://h:9200/?index=i", true},
		{"elasticsearch other index", "elasticsearch://h:9200/?index=i", "elasticsearch://h:9200/?index=j", false},
		{"couchdb database in the path", "couchdb://h:5984/db", "couchdb://h:5984/?database=db", true},
		{"couchbase bare and full collection", "couchbase://h/?bucket=b&collection=c", "couchbases://h:11207/?bucket=b&collection=_default.c", true},
		{"couchbase other bucket", "couchbase://h/?bucket=b1&collection=c", "couchbase://h/?bucket=b2&collection=c", false},
		{"neo4j default port", "neo4j://h/?label=L", "bolt://h:7687/?label=L", true},
		{"neo4j other database", "neo4j://h/?label=L&database=a", "neo4j://h/?label=L&database=b", false},
		{"hbase quorums with one common host", "hbase://a,b/?table=t", "hbase://b:2181/?table=t", true},
		{"hbase other table", "hbase://a/?table=t", "hbase://a/?table=u", false},
		{"dynamodb same region and table", "dynamodb://us-east-1/?table=t", "dynamodb://US-EAST-1/?table=t", true},
		{"dynamodb other region", "dynamodb://us-east-1/?table=t", "dynamodb://eu-west-1/?table=t", false},
		{"different drivers", "redis://h", "mongodb://h:6379", false},
		{"unparsable query", "redis://h/0?a=%zz", "redis://h:6379/0", false},
		{"no URL", "", "redis://h", false},
		{"IPv6 long and short form", "redis://[2001:db8::1]", "redis://[2001:0db8:0:0:0:0:0:1]:6379", true},
		{"IPv6 other address", "redis://[2001:db8::1]", "redis://[2001:db8::2]", false},
		{"IPv6 case", "redis://[2001:DB8::1]", "redis://[2001:db8::1]", true},
		{"trailing dot on a name", "redis://h.", "redis://h:6379", true},
		{"trailing dot with a port", "redis://h.:6380", "redis://h:6380", true},
		{"IPv4-mapped IPv6", "redis://[::ffff:10.0.0.1]", "redis://10.0.0.1", true},
		{"IPv4-mapped IPv6 loopback", "redis://[::ffff:127.0.0.1]", "redis://127.0.0.1", true},
		{"IPv4 other address", "redis://10.0.0.1", "redis://10.0.0.2", false},
		{"only a dot as host", "redis://.", "redis://.", false},
		{"couchdb default port", "couchdb://h/db", "couchdb://h:80/db", true},
		{"couchdb other port", "couchdb://h/db", "couchdb://h:5984/db", false},
		{"couchdbs default port", "couchdbs://h/db", "couchdbs://h:443/db", true},
		{"couchdbs and plain defaults differ", "couchdbs://h/db", "couchdb://h/db", false},
		{"elasticsearch default port", "elasticsearch://h/?index=i", "elasticsearch://h:80/?index=i", true},
		{"elasticsearch other port", "elasticsearch://h/?index=i", "elasticsearch://h:9200/?index=i", false},
		{"elasticsearch TLS default port", "elasticsearch+s://h/?index=i", "elasticsearch+s://h:443/?index=i", true},
		{"opensearch default port", "opensearch://h/?index=i", "opensearch://h:80/?index=i", true},
		{"opensearch other port", "opensearch://h/?index=i", "opensearch://h:9200/?index=i", false},
		{"opensearch TLS default port", "opensearch+s://h/?index=i", "elasticsearch+s://h:443/?index=i", true},
		{"empty user info", "redis://@h", "redis://h:6379", true},
		{"at sign in the password", "redis://u:p@ss@h", "redis://h:6379", true},
		{"no host", "redis://", "redis://", false},
		{"empty host item skipped in a list", "mongodb://a,,b/db?collection=c", "mongodb://b/db?collection=c", true},
		{"redis non-numeric database name", "redis://h/name", "redis://h:6379/name", true},
		{"redis other non-numeric database name", "redis://h/name", "redis://h/other", false},
		{"couchbase without bucket or collection", "couchbase://h", "couchbases://h:11207", true},
		{"couchbase no collection and the default collection", "couchbase://h", "couchbase://h/?collection=_default", true},
		{"file source", "file:///a.bson", "file:///a.bson", false},
		{"unknown scheme", "nope://h", "nope://h", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, sameContainer(endpoint{url: tc.a}, endpoint{url: tc.b}))
			require.Equal(t, tc.want, sameContainer(endpoint{url: tc.b}, endpoint{url: tc.a}), "symmetric")
		})
	}
}

func TestSplitEndpointURL(t *testing.T) {
	authority, path, q, ok := splitEndpointURL("mongodb://u:p@a,b/db/?collection=c#frag")
	require.True(t, ok)
	require.Equal(t, "u:p@a,b", authority)
	require.Equal(t, "db", path)
	require.Equal(t, "c", q.Get("collection"))

	_, _, _, ok = splitEndpointURL("redis://h/0?a=%zz")
	require.False(t, ok, "a query that does not parse")
}

func TestSameEndpointFallsBackToStrings(t *testing.T) {
	bad := "redis://h/0?a=%zz"
	require.True(t, sameEndpoint(endpoint{url: bad}, endpoint{url: bad}), "equal strings still match")
	require.False(t, sameEndpoint(endpoint{url: bad}, endpoint{url: "redis://h:6379/0"}))
	require.True(t, sameEndpoint(
		endpoint{handle: "a", url: "redis://h", address: ""},
		endpoint{handle: "b", url: "redis://h:6379/0"}), "two handles for one container")
	require.False(t, sameEndpoint(
		endpoint{handle: "a", url: "mongodb://h/db", address: "orders"},
		endpoint{handle: "b", url: "mongodb://h:27017/db", address: "items"}), "another address")
}

func TestSourceEndpoint(t *testing.T) {
	tests := []struct {
		name string
		cfg  config
		want endpoint
	}{
		{"a resolved source", config{handle: "m", url: "mongodb://h/db", address: "orders"}, endpoint{handle: "m", url: "mongodb://h/db", address: "orders"}},
		{"no resolved source", config{url: "mongodb://h/db", address: "orders"}, endpoint{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, sourceEndpoint(&tc.cfg))
		})
	}
}

func TestInsertReplaceOntoItsOwnSource(t *testing.T) {
	const refusal = "--replace would empty full before the copy reads it, because it is also the source"
	tests := []struct {
		name       string
		args       []string
		wantErr    string
		wantEvents []string
	}{
		{"replace onto the source", []string{"--src", "full", "--insert", "full", "--replace", "--force"}, refusal, nil},
		{"dry-run replace onto the source", []string{"--src", "full", "--insert", "full", "--replace", "--dry-run"}, refusal, nil},
		{"replace onto an alias of the source", []string{"--src", "full", "--insert", "alias", "--replace", "--force"}, "--replace would empty alias before the copy reads it, because it is also the source", nil},
		{"replace onto another host", []string{"--src", "full", "--insert", "other", "--replace", "--force"}, "", []string{"clear", "put"}},
		{"plain insert onto the source", []string{"--src", "full", "--insert", "full"}, "", []string{"put"}},
		{"replace onto another source", []string{"--src", "full", "--insert", "put", "--replace", "--dry-run"}, "--replace: destination put (mvput) cannot be cleared", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			log := useMoveDrivers(t)
			root, _ := newRootCmd()
			out, err := runCmd(t, root, append([]string{"--timeout", "5s"}, tc.args...)...)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
				require.NotContains(t, err.Error()+out, "hunter2")
			}
			require.Equal(t, tc.wantEvents, log.events, "the destination is not cleared or written")
		})
	}
}

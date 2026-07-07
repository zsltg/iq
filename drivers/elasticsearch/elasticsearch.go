package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/render"
)

// scanBatch is the default page size for ScanBatches, TypedScan, and ScanFiltered:
// how many documents to fetch per search page, bounding streaming memory.
const scanBatch = 100

// keepAlive is the point-in-time lifetime requested for a scan. Each search page
// refreshes it, so it need only outlast the gap between pages, not the whole scan.
const keepAlive = "1m"

// errNoIndex is returned when an index-scoped operation runs without an index
// selected. It is a sentinel so the CLI can surface a clear hint.
var errNoIndex = errors.New("elasticsearch: no index selected; address it as handle.index or set ?index= in the source url")

// Store adapts one Elasticsearch index to the query ports. The jq and write paths
// are scoped to a single index (the keyspace); the raw path runs a _search and the
// inspect path reads server- and index-level metadata. exact holds the top-level
// fields whose mapping makes an equality pushable as a term query (read once at
// open); it is nil when no index is selected or the mapping could not be read, in
// which case equality simply is not pushed.
type Store struct {
	es       *elasticsearch.Client
	index    string
	pageSize int
	decimal  numfmt.DecimalMode
	exact    map[string]exactField
}

// connConfig is the parsed form of an elasticsearch:// source URL: the server
// address the client connects to, the basic-auth credentials, and the default index.
type connConfig struct {
	addr     string
	username string
	password string
	index    string
}

// Open connects to the Elasticsearch server named by an elasticsearch:// (or
// elasticsearch+s://) URL and verifies the connection with an info request so a bad
// URL, unreachable server, or bad credentials fails fast. The index (the jq
// keyspace, may be empty for raw/inspect-only use) is the dotted address override
// when non-empty, else the URL's ?index= default, else the URL path. When trace is
// non-nil, each HTTP request is logged to it (the CLI's --verbose trace) as
// method+path only, so no credential (carried in the Authorization header) is ever
// written. dec chooses how fractional numbers are presented to the filter.
func Open(ctx context.Context, rawURL, address string, trace io.Writer, dec numfmt.DecimalMode) (*Store, error) {
	cc, err := parseURL(rawURL, address)
	if err != nil {
		return nil, err
	}
	cfg := elasticsearch.Config{
		Addresses: []string{cc.addr},
		Username:  cc.username,
		Password:  cc.password,
	}
	if trace != nil {
		cfg.Transport = &traceTransport{w: trace}
	}
	es, err := elasticsearch.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("connect elasticsearch: %w", err)
	}
	s := &Store{es: es, index: cc.index, pageSize: scanBatch, decimal: dec}

	res, err := es.Info(es.Info.WithContext(ctx))
	if err := finish(res, err, "connect", nil); err != nil {
		return nil, err
	}
	if cc.index != "" {
		// Best-effort: the field types drive equality pushdown only. If the mapping
		// cannot be read (permissions, an absent index), equality is simply not
		// pushed and the full jq still runs client-side — never a wrong result.
		s.exact, _ = s.readExactFields(ctx)
	}
	return s, nil
}

// parseURL splits an elasticsearch:// source URL into the server address, basic-auth
// credentials, and the default index. elasticsearch:// maps to an http:// address
// and elasticsearch+s:// to https://; the index is the dotted address override, else
// ?index=, else the URL path. A missing scheme, host, or malformed index is an error
// so a bad source fails at parse time.
func parseURL(rawURL, address string) (connConfig, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return connConfig{}, fmt.Errorf("parse elasticsearch url: %w", err)
	}
	var scheme string
	switch u.Scheme {
	case "elasticsearch":
		scheme = "http"
	case "elasticsearch+s":
		scheme = "https"
	default:
		return connConfig{}, fmt.Errorf("elasticsearch url must use elasticsearch:// or elasticsearch+s://, got %q", u.Scheme)
	}
	if u.Host == "" {
		return connConfig{}, fmt.Errorf("elasticsearch url must name a host, e.g. elasticsearch://localhost:9200/?index=books")
	}

	q := u.Query()
	index := address
	if index == "" {
		index = q.Get("index")
	}
	if index == "" {
		// Lenient: accept the index in the path too (elasticsearch://host/books), as
		// long as it names a single index.
		if p := strings.Trim(u.Path, "/"); p != "" && !strings.Contains(p, "/") {
			index = p
		}
	}
	if index != "" {
		if err := validateIndex(index); err != nil {
			return connConfig{}, err
		}
	}

	cc := connConfig{addr: (&url.URL{Scheme: scheme, Host: u.Host}).String(), index: index}
	if u.User != nil {
		cc.username = u.User.Username()
		cc.password, _ = u.User.Password()
	}
	return cc, nil
}

// badIndexChars are the bytes Elasticsearch forbids in an index name; ',' and '*'
// also matter to us because they would select several indices, breaking the
// single-keyspace model. Rejecting them up front also keeps the name safe to place
// in a request path.
const badIndexChars = "\\/*?\"<>| ,#:"

// validateIndex rejects an index name Elasticsearch would refuse, or one that would
// address more than one index, before it reaches a request path — the "never build a
// query from unsanitized input" rule for the index segment.
func validateIndex(name string) error {
	if name == "" {
		return errors.New("elasticsearch index name is empty")
	}
	if len(name) > 255 {
		return fmt.Errorf("elasticsearch index name %q exceeds 255 bytes", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("elasticsearch index name %q is invalid", name)
	}
	if strings.ToLower(name) != name {
		return fmt.Errorf("elasticsearch index name %q must be lowercase", name)
	}
	if strings.ContainsAny(name, badIndexChars) {
		return fmt.Errorf("elasticsearch index name %q contains an invalid character", name)
	}
	switch name[0] {
	case '-', '_', '+':
		return fmt.Errorf("elasticsearch index name %q must not start with %q", name, string(name[0]))
	}
	return nil
}

// Get fetches the documents whose _id matches one of keys and returns them keyed by
// _id string. A key with no document (missing) maps to nil. Empty keys short-circuit
// with no round-trip.
func (s *Store) Get(ctx context.Context, keys []string) (map[string]any, error) {
	if s.index == "" {
		return nil, errNoIndex
	}
	out := make(map[string]any, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	body, err := jsonReader(map[string]any{"ids": keys})
	if err != nil {
		return nil, err
	}
	var mr struct {
		Docs []struct {
			ID     string          `json:"_id"`
			Found  bool            `json:"found"`
			Source json.RawMessage `json:"_source"`
		} `json:"docs"`
	}
	res, err := s.es.Mget(body, s.es.Mget.WithIndex(s.index), s.es.Mget.WithContext(ctx))
	if err := finish(res, err, "mget", &mr); err != nil {
		return nil, err
	}
	for _, d := range mr.Docs {
		if !d.Found {
			out[d.ID] = nil
			continue
		}
		doc, err := decodeSource(d.Source, d.ID, s.decimal)
		if err != nil {
			return nil, err
		}
		out[d.ID] = doc
	}
	// Defensive: any key the response omitted reads as absent.
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			out[k] = nil
		}
	}
	return out, nil
}

// ScanBatches streams the whole index, handing the caller each page of
// {_id: document}. It pages with a point-in-time and search_after (keyset
// pagination), so it never re-reads from an offset and holds only one page in
// memory. Bounded by ctx; stops at the first error from fn or the driver.
func (s *Store) ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error {
	return s.pagedSearch(ctx, nil, fn)
}

// searchResponse is the subset of a _search reply the scan needs: the (possibly
// refreshed) point-in-time id, and each hit's _id, _source, and sort values (fed
// back as the next page's search_after).
type searchResponse struct {
	PitID string `json:"pit_id"`
	Hits  struct {
		Total struct {
			Value int64 `json:"value"`
		} `json:"total"`
		Hits []struct {
			ID     string          `json:"_id"`
			Source json.RawMessage `json:"_source"`
			Sort   json.RawMessage `json:"sort"`
		} `json:"hits"`
	} `json:"hits"`
}

// pagedSearch pages a _search over an optional query, handing fn each page of
// {_id: document}. It opens a point-in-time, sorts by _shard_doc (a total, stable
// tiebreaker only defined within a PIT), and advances with search_after, so a
// resized index cannot make the walk skip or loop. query is nil for a full scan and
// a bool query for a pushed-down filtered scan. The PIT is always closed, even on
// error or a cancelled ctx.
func (s *Store) pagedSearch(ctx context.Context, query map[string]any, fn func(batch map[string]any) error) error {
	if s.index == "" {
		return errNoIndex
	}
	pit, err := s.openPIT(ctx)
	if err != nil {
		return err
	}
	// Close the latest PIT id (a search may rotate it) on every exit path, detached
	// from ctx so a cancelled scan still frees the server-side resource.
	defer func() { s.closePIT(context.WithoutCancel(ctx), pit) }()

	var after json.RawMessage
	for {
		body := map[string]any{
			"size":             s.pageSize,
			"track_total_hits": false,
			"sort":             []any{map[string]any{"_shard_doc": "asc"}},
			"pit":              map[string]any{"id": pit, "keep_alive": keepAlive},
		}
		if query != nil {
			body["query"] = query
		}
		if after != nil {
			body["search_after"] = after
		}
		r, err := jsonReader(body)
		if err != nil {
			return err
		}
		var sr searchResponse
		res, err := s.es.Search(s.es.Search.WithContext(ctx), s.es.Search.WithBody(r))
		if err := finish(res, err, "search", &sr); err != nil {
			return err
		}
		// A PIT search always echoes the (possibly rotated) point-in-time id, and the
		// docs say to always reuse the most recent one, so adopt it unconditionally.
		pit = sr.PitID
		hits := sr.Hits.Hits
		if len(hits) == 0 {
			return nil
		}
		page := make(map[string]any, len(hits))
		for _, h := range hits {
			doc, err := decodeSource(h.Source, h.ID, s.decimal)
			if err != nil {
				return err
			}
			page[h.ID] = doc
			after = h.Sort
		}
		if err := fn(page); err != nil {
			return err
		}
		// A page shorter than the requested size is the last one.
		if len(hits) < s.pageSize {
			return nil
		}
	}
}

// openPIT opens a point-in-time over the index and returns its id.
func (s *Store) openPIT(ctx context.Context) (string, error) {
	var pr struct {
		ID string `json:"id"`
	}
	res, err := s.es.OpenPointInTime([]string{s.index}, keepAlive, s.es.OpenPointInTime.WithContext(ctx))
	if err := finish(res, err, "open point-in-time", &pr); err != nil {
		return "", err
	}
	if pr.ID == "" {
		return "", errors.New("elasticsearch open point-in-time: empty pit id")
	}
	return pr.ID, nil
}

// closePIT releases a point-in-time. It is best-effort: a PIT expires on its own
// keep-alive, so a failed close is not fatal to a scan that already finished. It
// reuses finish to drain and close the reply, so there is no bespoke response
// handling of its own. The id is always non-empty here (openPIT guarantees it).
func (s *Store) closePIT(ctx context.Context, id string) {
	body, _ := json.Marshal(map[string]string{"id": id})
	res, err := s.es.ClosePointInTime(s.es.ClosePointInTime.WithContext(ctx), s.es.ClosePointInTime.WithBody(bytes.NewReader(body)))
	_ = finish(res, err, "close point-in-time", nil)
}

// EstimateCount returns the index's document count (GET /{index}/_count), a cheap
// approximate total for a full scan's progress. It may be stale under concurrent
// writes, so the caller treats it as a hint. A missing index is the same error the
// scan paths return.
func (s *Store) EstimateCount(ctx context.Context) (int64, error) {
	if s.index == "" {
		return 0, errNoIndex
	}
	var cr struct {
		Count int64 `json:"count"`
	}
	res, err := s.es.Count(s.es.Count.WithIndex(s.index), s.es.Count.WithContext(ctx))
	if err := finish(res, err, "count", &cr); err != nil {
		return 0, err
	}
	return cr.Count, nil
}

// Query runs a raw _search against the index. args must be a single JSON document:
// either a full search body (`{"query":{…},"size":…,"aggs":…}`) or a bare query
// object (`{"match":{"title":"dune"}}`), which is wrapped as `{"query":…}`. It is
// decoded and passed as a parameter object — never string-built — and returns the
// whole _search reply (hits, aggregations, and all) decoded with exact number
// precision, so it is a faithful escape hatch for anything the jq read path does not
// cover.
func (s *Store) Query(ctx context.Context, args []string) (any, error) {
	if s.index == "" {
		return nil, errNoIndex
	}
	if len(args) != 1 {
		return nil, errors.New("elasticsearch raw expects one JSON search body")
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(args[0]), &body); err != nil {
		return nil, fmt.Errorf("parse elasticsearch search body: %w", err)
	}
	if _, ok := body["query"]; !ok {
		body = map[string]any{"query": body}
	}
	r, err := jsonReader(body)
	if err != nil {
		return nil, err
	}
	res, err := s.es.Search(s.es.Search.WithIndex(s.index), s.es.Search.WithBody(r), s.es.Search.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("elasticsearch search: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.IsError() {
		return nil, apiError(res, "search")
	}
	dec := json.NewDecoder(res.Body)
	dec.UseNumber()
	var reply any
	if err := dec.Decode(&reply); err != nil {
		return nil, fmt.Errorf("elasticsearch search: decode response: %w", err)
	}
	return convertNumbers(reply, s.decimal), nil
}

// FormatRaw renders a raw _search reply as indented JSON, the natural form for a
// document store, syntax-highlighted when colored is set.
func (s *Store) FormatRaw(v any, colored bool) string {
	out, err := render.JSON(v, colored)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return out
}

// Close releases the client's resources. The HTTP client needs no explicit teardown,
// so this is a no-op that satisfies the port.
func (s *Store) Close() error {
	return nil
}

// jsonReader marshals v to a JSON reader for a request body. A json.RawMessage value
// (the search_after cursor) is emitted verbatim.
func jsonReader(v any) (*bytes.Reader, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode elasticsearch request: %w", err)
	}
	return bytes.NewReader(b), nil
}

// finish handles the common (*esapi.Response, error) result of an esapi call: it maps
// a transport error and an HTTP error status to a clean message (never leaking the
// raw response body), and otherwise decodes the JSON body into out when non-nil. It
// always closes the body.
func finish(res *esapi.Response, err error, op string, out any) error {
	if err != nil {
		return fmt.Errorf("elasticsearch %s: %w", op, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.IsError() {
		return apiError(res, op)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("elasticsearch %s: decode response: %w", op, err)
	}
	return nil
}

// apiError turns an Elasticsearch error response into a safe, concise message: the
// error type and reason it reports, or the status code when the body is not the
// expected error envelope. The raw body is never surfaced verbatim.
func apiError(res *esapi.Response, op string) error {
	var e struct {
		Error struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&e); err == nil && e.Error.Type != "" {
		if e.Error.Reason != "" {
			return fmt.Errorf("elasticsearch %s: %s: %s", op, e.Error.Type, e.Error.Reason)
		}
		return fmt.Errorf("elasticsearch %s: %s", op, e.Error.Type)
	}
	return fmt.Errorf("elasticsearch %s: unexpected status %d", op, res.StatusCode)
}

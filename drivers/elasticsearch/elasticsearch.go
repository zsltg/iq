package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/rawpred"
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
var errNoIndex = errors.New("no index selected; address it as handle.index or set ?index= in the source url")

// Store adapts one Elasticsearch or OpenSearch index to the query ports. The jq and
// write paths are scoped to a single index (the keyspace); the raw path runs a
// _search and the inspect path reads server- and index-level metadata. The wire
// client (Elasticsearch or OpenSearch) is chosen by the source scheme and hidden
// behind the esClient port, so everything below the transport is shared. exact holds
// the top-level fields whose mapping makes an equality pushable as a term query (read
// once at open); it is nil when no index is selected or the mapping could not be read,
// in which case equality simply is not pushed.
type Store struct {
	client   esClient
	index    string
	pageSize int
	decimal  numfmt.DecimalMode
	exact    map[string]exactField
	// prefilterChecked counts hits the client-side raw-byte prefilter evaluated (ran
	// rawpred over) across this store's filtered scans, and prefilterSkipped counts the
	// subset it dropped before decode. They are diagnostic counters the package's tests
	// read to prove the prefilter engages exactly when it should — checked stays zero
	// on a bypassed scan and rises on a prefiltered one — and never part of the public
	// API; no result depends on either.
	prefilterChecked int
	prefilterSkipped int
}

// connConfig is the parsed form of a source URL: the wire flavor, the server address
// the client connects to, the basic-auth credentials, and the default index.
type connConfig struct {
	flavor   flavor
	addr     string
	username string
	password string
	index    string
}

// Open connects to the server named by an elasticsearch:// / elasticsearch+s:// /
// opensearch:// / opensearch+s:// URL and verifies the connection with an info request
// so a bad URL, unreachable server, or bad credentials fails fast. The index (the jq
// keyspace, may be empty for raw/inspect-only use) is the dotted address override when
// non-empty, else the URL's ?index= default, else the URL path. When trace is non-nil,
// each HTTP request is logged to it (the CLI's --verbose trace) as method+path only,
// so no credential (carried in the Authorization header) is ever written. dec chooses
// how fractional numbers are presented to the filter.
func Open(ctx context.Context, rawURL, address string, trace io.Writer, dec numfmt.DecimalMode) (*Store, error) {
	cc, err := parseURL(rawURL, address)
	if err != nil {
		return nil, err
	}
	client, err := newClient(cc, trace)
	if err != nil {
		return nil, err
	}
	s := &Store{client: client, index: cc.index, pageSize: scanBatch, decimal: dec}

	if err := s.request(ctx, "connect", http.MethodGet, "/", nil, nil); err != nil {
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

// parseURL splits a source URL into the wire flavor, server address, basic-auth
// credentials, and the default index. elasticsearch:// / opensearch:// map to an
// http:// address and the +s variants to https://; the index is the dotted address
// override, else ?index=, else the URL path. A missing scheme, host, or malformed
// index is an error so a bad source fails at parse time.
func parseURL(rawURL, address string) (connConfig, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return connConfig{}, fmt.Errorf("parse source url: %w", err)
	}
	scheme, fl, err := schemeFlavor(u.Scheme)
	if err != nil {
		return connConfig{}, err
	}
	if u.Host == "" {
		return connConfig{}, fmt.Errorf("url must name a host, e.g. elasticsearch://localhost:9200/?index=books")
	}
	index, err := resolveIndex(address, u)
	if err != nil {
		return connConfig{}, err
	}

	cc := connConfig{flavor: fl, addr: (&url.URL{Scheme: scheme, Host: u.Host}).String(), index: index}
	if u.User != nil {
		cc.username = u.User.Username()
		cc.password, _ = u.User.Password()
	}
	return cc, nil
}

// schemeFlavor maps a source URL scheme to the HTTP scheme of the server address and
// the wire flavor. The +s variants use https.
func schemeFlavor(scheme string) (string, flavor, error) {
	switch scheme {
	case "elasticsearch":
		return "http", flavorES, nil
	case "elasticsearch+s":
		return "https", flavorES, nil
	case "opensearch":
		return "http", flavorOS, nil
	case "opensearch+s":
		return "https", flavorOS, nil
	}
	return "", 0, fmt.Errorf("url must use elasticsearch://, elasticsearch+s://, opensearch://, or opensearch+s://, got %q", scheme)
}

// resolveIndex picks the index: the dotted address override, else ?index=, else a
// single-segment path. A name it finds must pass validateIndex. No index is allowed.
func resolveIndex(address string, u *url.URL) (string, error) {
	index := address
	if index == "" {
		index = u.Query().Get("index")
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
			return "", err
		}
	}
	return index, nil
}

// badIndexChars are the bytes Elasticsearch and OpenSearch forbid in an index name;
// ',' and '*' also matter to us because they would select several indices, breaking
// the single-keyspace model. Rejecting them up front also keeps the name safe to
// place in a request path.
const badIndexChars = "\\/*?\"<>| ,#:"

// validateIndex rejects an index name the backend would refuse, or one that would
// address more than one index, before it reaches a request path — the "never build a
// query from unsanitized input" rule for the index segment.
func validateIndex(name string) error {
	if name == "" {
		return errors.New("index name is empty")
	}
	if len(name) > 255 {
		return fmt.Errorf("index name %q exceeds 255 bytes", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("index name %q is invalid", name)
	}
	if strings.ToLower(name) != name {
		return fmt.Errorf("index name %q must be lowercase", name)
	}
	if strings.ContainsAny(name, badIndexChars) {
		return fmt.Errorf("index name %q contains an invalid character", name)
	}
	switch name[0] {
	case '-', '_', '+':
		return fmt.Errorf("index name %q must not start with %q", name, string(name[0]))
	}
	return nil
}

// Get fetches the documents whose _id matches one of keys and returns them keyed by
// _id string. A key with no document (missing) is absent from the map. Empty keys short-circuit
// with no round-trip.
func (s *Store) Get(ctx context.Context, keys []string) (map[string]any, error) {
	if s.index == "" {
		return nil, errNoIndex
	}
	out := make(map[string]any, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	body, err := json.Marshal(map[string]any{"ids": keys})
	if err != nil {
		return nil, fmt.Errorf("encode mget: %w", err)
	}
	var mr struct {
		Docs []struct {
			ID     string          `json:"_id"`
			Found  bool            `json:"found"`
			Source json.RawMessage `json:"_source"`
		} `json:"docs"`
	}
	if err := s.request(ctx, "mget", http.MethodPost, "/"+s.index+"/_mget", body, &mr); err != nil {
		return nil, err
	}
	for _, d := range mr.Docs {
		if !d.Found {
			continue
		}
		doc, err := decodeSource(d.Source, d.ID, s.decimal)
		if err != nil {
			return nil, err
		}
		out[d.ID] = doc
	}
	return out, nil
}

// ScanBatches streams the whole index, handing the caller each page of
// {_id: document}. It pages with a point-in-time and search_after (keyset
// pagination), so it never re-reads from an offset and holds only one page in
// memory. Bounded by ctx; stops at the first error from fn or the driver.
func (s *Store) ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error {
	return s.pagedSearch(ctx, nil, nil, fn)
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
		Hits []searchHit `json:"hits"`
	} `json:"hits"`
}

// searchHit is one hit of a _search reply: the document id, its raw _source, and its
// sort values.
type searchHit struct {
	ID     string          `json:"_id"`
	Source json.RawMessage `json:"_source"`
	Sort   json.RawMessage `json:"sort"`
}

// pagedSearch pages a _search over an optional query, handing fn each page of
// {_id: document}. It opens a point-in-time, sorts by the flavor's keyset tiebreaker
// (a total, stable order only defined within a PIT), and advances with search_after,
// so a resized index cannot make the walk skip or loop. query is nil for a full scan
// and a bool query for a pushed-down filtered scan. The PIT is always closed, even on
// error or a cancelled ctx.
//
// When prefilter is non-nil, each hit's raw _source is run through it before decode,
// and a hit the prepared matcher proves the predicate rejects is skipped (counted in
// prefilterSkipped) rather than decoded and delivered — a byte-level drop that never
// changes results because the matcher's predicate is a conservative superset the caller
// re-runs in full. The matcher is built once per scan by ScanFiltered (so its Regex
// patterns compile once, not per hit); the keyset cursor advances past every hit,
// skipped or kept, so pagination never re-reads or loops; a page emptied entirely by
// the prefilter is simply not handed to fn, preserving the "a scan never yields an empty
// batch" contract.
func (s *Store) pagedSearch(ctx context.Context, query map[string]any, prefilter *rawpred.Matcher, fn func(batch map[string]any) error) error {
	if s.index == "" {
		return errNoIndex
	}
	pit, err := s.client.openPIT(ctx, s.index)
	if err != nil {
		return err
	}
	// Close the latest PIT id (a search may rotate it) on every exit path, detached
	// from ctx so a cancelled scan still frees the server-side resource.
	defer func() { s.client.closePIT(context.WithoutCancel(ctx), pit) }()

	var after json.RawMessage
	for {
		raw, err := s.searchBody(pit, query, after)
		if err != nil {
			return err
		}
		var sr searchResponse
		if err := s.request(ctx, "search", http.MethodPost, "/_search", raw, &sr); err != nil {
			return err
		}
		// A PIT search always echoes the (possibly rotated) point-in-time id, and the
		// docs say to always reuse the most recent one, so adopt it unconditionally.
		pit = sr.PitID
		hits := sr.Hits.Hits
		if len(hits) == 0 {
			return nil
		}
		var page map[string]any
		page, after, err = s.pageOf(hits, prefilter)
		if err != nil {
			return err
		}
		// A page the prefilter emptied is never handed to fn: a scan never yields an
		// empty batch.
		if len(page) > 0 {
			if err := fn(page); err != nil {
				return err
			}
		}
		// A page shorter than the requested size is the last one.
		if len(hits) < s.pageSize {
			return nil
		}
	}
}

// searchBody encodes one page request: the page size, the flavor sort, and the
// point-in-time. The query is present only for a filtered scan, and search_after only
// after the first page.
func (s *Store) searchBody(pit string, query map[string]any, after json.RawMessage) ([]byte, error) {
	body := map[string]any{
		"size":             s.pageSize,
		"track_total_hits": false,
		"sort":             s.client.scanSort(),
		"pit":              map[string]any{"id": pit, "keep_alive": keepAlive},
	}
	if query != nil {
		body["query"] = query
	}
	if after != nil {
		body["search_after"] = after
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode search: %w", err)
	}
	return raw, nil
}

// pageOf decodes the hits the prefilter keeps into a page of {_id: document}. It also
// returns the sort values of the last hit, kept or skipped, so a prefiltered-out
// document never stalls or rewinds the keyset walk.
func (s *Store) pageOf(hits []searchHit, prefilter *rawpred.Matcher) (map[string]any, json.RawMessage, error) {
	page := make(map[string]any, len(hits))
	var after json.RawMessage
	for _, h := range hits {
		after = h.Sort
		if !s.keep(h, prefilter) {
			continue
		}
		doc, err := decodeSource(h.Source, h.ID, s.decimal)
		if err != nil {
			return nil, nil, err
		}
		page[h.ID] = doc
	}
	return page, after, nil
}

// keep reports whether a hit goes on to the decode. With a matcher, it counts the hit
// as checked, and as skipped when the matcher proves the predicate rejects it.
func (s *Store) keep(h searchHit, prefilter *rawpred.Matcher) bool {
	if prefilter == nil {
		return true
	}
	s.prefilterChecked++
	if prefilter.Match(h.Source) == rawpred.CannotMatch {
		s.prefilterSkipped++
		return false
	}
	return true
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
	if err := s.request(ctx, "count", http.MethodGet, "/"+s.index+"/_count", nil, &cr); err != nil {
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
		return nil, errors.New("raw exec expects one JSON search body")
	}
	raw, err := searchRequest(args[0])
	if err != nil {
		return nil, err
	}
	res, err := s.do(ctx, http.MethodPost, "/"+s.index+"/_search", "application/json", raw)
	if err != nil {
		return nil, fmt.Errorf("%s search: %w", s.client.label(), err)
	}
	return s.decodeReply(res)
}

// searchRequest parses a raw search argument and encodes the request body. A bare
// query object is wrapped as {"query":...}.
func searchRequest(arg string) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal([]byte(arg), &body); err != nil {
		return nil, fmt.Errorf("parse search body: %w", err)
	}
	if _, ok := body["query"]; !ok {
		body = map[string]any{"query": body}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode search body: %w", err)
	}
	return raw, nil
}

// decodeReply turns a _search reply into a value with exact numbers, and closes the
// body. A non-2xx status becomes the labelled API error.
func (s *Store) decodeReply(res *http.Response) (any, error) {
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		return nil, apiError(res, s.client.label(), "search")
	}
	dec := json.NewDecoder(res.Body)
	dec.UseNumber()
	var reply any
	if err := dec.Decode(&reply); err != nil {
		return nil, fmt.Errorf("%s search: decode response: %w", s.client.label(), err)
	}
	return numfmt.ConvertNumbers(reply, s.decimal), nil
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

// Close releases the client's resources.
func (s *Store) Close() error {
	return s.client.close()
}

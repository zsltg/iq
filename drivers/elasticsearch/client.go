package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/opensearch-project/opensearch-go/v4"
)

// flavor names the wire dialect a source speaks. Elasticsearch and OpenSearch share
// the same REST surface for everything this driver does except the point-in-time
// lifecycle and the keyset sort, so one Store serves both behind the esClient port.
type flavor int

const (
	flavorES flavor = iota // Elasticsearch, via the go-elasticsearch client.
	flavorOS               // OpenSearch, via the opensearch-go client.
)

// esClient is the pluggable transport a Store speaks. The generic operations (mget,
// search, count, bulk, delete-by-query, mapping, cat, info) are identical across
// Elasticsearch and OpenSearch, so the Store builds those requests itself and calls
// perform. Only the point-in-time create/delete endpoints and the search_after sort
// tiebreaker differ, so those are methods each implementation owns.
type esClient interface {
	// perform issues one request. The request carries only a path; the client's
	// transport fills in the scheme, host, and credentials.
	perform(req *http.Request) (*http.Response, error)
	// label is the backend name used in error messages and the --verbose trace.
	label() string
	// openPIT opens a point-in-time over the index and returns its id.
	openPIT(ctx context.Context, index string) (string, error)
	// closePIT releases a point-in-time (best-effort).
	closePIT(ctx context.Context, pitID string)
	// scanSort is the total, stable sort a keyset scan pages by inside a PIT.
	scanSort() []any
	// close releases the client's resources.
	close() error
}

// newClient builds the wire client a source's flavor selects. Both clients take the
// same config shape (addresses, basic-auth credentials, an optional trace transport),
// so the only difference is which constructor runs.
func newClient(cc connConfig, trace io.Writer) (esClient, error) {
	switch cc.flavor {
	case flavorOS:
		cfg := opensearch.Config{
			Addresses: []string{cc.addr},
			Username:  cc.username,
			Password:  cc.password,
		}
		if trace != nil {
			cfg.Transport = &traceTransport{w: trace, prefix: "os"}
		}
		c, err := opensearch.NewClient(cfg)
		if err != nil {
			return nil, fmt.Errorf("connect opensearch: %w", err)
		}
		return osFlavor{os: c}, nil
	default:
		cfg := elasticsearch.Config{
			Addresses: []string{cc.addr},
			Username:  cc.username,
			Password:  cc.password,
		}
		if trace != nil {
			cfg.Transport = &traceTransport{w: trace, prefix: "es"}
		}
		c, err := elasticsearch.NewClient(cfg)
		if err != nil {
			return nil, fmt.Errorf("connect elasticsearch: %w", err)
		}
		return esFlavor{es: c}, nil
	}
}

// esFlavor speaks to Elasticsearch through the go-elasticsearch client. Its PIT
// endpoints are `_pit`, and it pages by `_shard_doc` (a stable per-PIT tiebreaker).
type esFlavor struct {
	es *elasticsearch.Client
}

func (c esFlavor) perform(req *http.Request) (*http.Response, error) { return c.es.Perform(req) }
func (c esFlavor) label() string                                     { return "elasticsearch" }
func (c esFlavor) scanSort() []any                                   { return []any{map[string]any{"_shard_doc": "asc"}} }
func (c esFlavor) close() error                                      { return nil }

func (c esFlavor) openPIT(ctx context.Context, index string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/"+index+"/_pit?keep_alive="+keepAlive, nil)
	if err != nil {
		return "", err
	}
	var pr struct {
		ID string `json:"id"`
	}
	res, perr := c.es.Perform(req)
	if err := decodeInto(res, perr, c.label(), "open point-in-time", &pr); err != nil {
		return "", err
	}
	if pr.ID == "" {
		return "", errors.New("elasticsearch open point-in-time: empty pit id")
	}
	return pr.ID, nil
}

func (c esFlavor) closePIT(ctx context.Context, pitID string) {
	// A static method+path request cannot fail to build, so the error is ignored (the
	// close is best-effort anyway) rather than left as a dead, untestable branch.
	body, _ := json.Marshal(map[string]string{"id": pitID})
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, "/_pit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := c.es.Perform(req)
	_ = decodeInto(res, err, c.label(), "close point-in-time", nil)
}

// osFlavor speaks to OpenSearch through the opensearch-go client (a fork of
// go-elasticsearch without the product check that refuses non-Elasticsearch servers).
// OpenSearch's PIT endpoints are `_search/point_in_time` and its delete body keys the
// id as `pit_id` in an array. OpenSearch forked before Elasticsearch's `_shard_doc`
// sort existed, so a keyset scan pages by `_id` — globally unique, so it is a correct
// total tiebreaker across any number of shards.
type osFlavor struct {
	os *opensearch.Client
}

func (c osFlavor) perform(req *http.Request) (*http.Response, error) { return c.os.Perform(req) }
func (c osFlavor) label() string                                     { return "opensearch" }
func (c osFlavor) scanSort() []any                                   { return []any{map[string]any{"_id": "asc"}} }
func (c osFlavor) close() error                                      { return nil }

func (c osFlavor) openPIT(ctx context.Context, index string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/"+index+"/_search/point_in_time?keep_alive="+keepAlive, nil)
	if err != nil {
		return "", err
	}
	var pr struct {
		PitID string `json:"pit_id"`
	}
	res, perr := c.os.Perform(req)
	if err := decodeInto(res, perr, c.label(), "open point-in-time", &pr); err != nil {
		return "", err
	}
	if pr.PitID == "" {
		return "", errors.New("opensearch open point-in-time: empty pit id")
	}
	return pr.PitID, nil
}

func (c osFlavor) closePIT(ctx context.Context, pitID string) {
	// A static method+path request cannot fail to build, so the error is ignored (the
	// close is best-effort anyway) rather than left as a dead, untestable branch.
	body, _ := json.Marshal(map[string]any{"pit_id": []string{pitID}})
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, "/_search/point_in_time", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := c.os.Perform(req)
	_ = decodeInto(res, err, c.label(), "close point-in-time", nil)
}

// request builds one JSON request against a relative path and performs it, decoding
// the reply into out (nil discards the body). op names the operation for errors.
func (s *Store) request(ctx context.Context, op, method, path string, body []byte, out any) error {
	res, err := s.do(ctx, method, path, "application/json", body)
	return s.finish(res, err, op, out)
}

// do builds an *http.Request carrying only a path — the client's transport fills the
// scheme, host, and credentials — and performs it through the pluggable client.
func (s *Store) do(ctx context.Context, method, path, contentType string, body []byte) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, path, r)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	// A body-bearing request needs its Content-Type; Elasticsearch and OpenSearch
	// reject a JSON/NDJSON body sent without one.
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	return s.client.perform(req)
}

// finish decodes a reply using the store's backend label for error messages.
func (s *Store) finish(res *http.Response, err error, op string, out any) error {
	return decodeInto(res, err, s.client.label(), op, out)
}

// decodeInto maps a transport error and an HTTP error status to a clean, labelled
// message (never leaking the raw response body), and otherwise decodes the JSON body
// into out when non-nil. It always closes the body.
func decodeInto(res *http.Response, err error, label, op string, out any) error {
	if err != nil {
		return fmt.Errorf("%s %s: %w", label, op, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		return apiError(res, label, op)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", label, op, err)
	}
	return nil
}

// apiError turns an error response into a safe, concise message: the error type and
// reason it reports, or the status code when the body is not the expected envelope.
func apiError(res *http.Response, label, op string) error {
	var e struct {
		Error struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&e); err == nil && e.Error.Type != "" {
		if e.Error.Reason != "" {
			return fmt.Errorf("%s %s: %s: %s", label, op, e.Error.Type, e.Error.Reason)
		}
		return fmt.Errorf("%s %s: %s", label, op, e.Error.Type)
	}
	return fmt.Errorf("%s %s: unexpected status %d", label, op, res.StatusCode)
}

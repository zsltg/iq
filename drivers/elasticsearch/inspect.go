package elasticsearch

import (
	"context"
	"encoding/json"
	"net/http"
)

// InspectServer returns the node's identity and version — its node and cluster names,
// the distribution version, and the Lucene version — the analogue of a buildInfo
// probe. It needs no index.
func (s *Store) InspectServer(ctx context.Context) (any, error) {
	var info struct {
		Name        string `json:"name"`
		ClusterName string `json:"cluster_name"`
		Version     struct {
			Number        string `json:"number"`
			LuceneVersion string `json:"lucene_version"`
		} `json:"version"`
		Tagline string `json:"tagline"`
	}
	if err := s.request(ctx, "server info", http.MethodGet, "/", nil, &info); err != nil {
		return nil, err
	}
	return map[string]any{
		"name":          info.Name,
		"cluster":       info.ClusterName,
		"version":       info.Version.Number,
		"luceneVersion": info.Version.LuceneVersion,
		"tagline":       info.Tagline,
	}, nil
}

// InspectIndices lists the server's indices with their basic stats (the _cat/indices
// table as JSON), the analogue of listing a keyspace's tables. It needs no index.
func (s *Store) InspectIndices(ctx context.Context) (any, error) {
	var rows []map[string]any
	if err := s.request(ctx, "list indices", http.MethodGet, "/_cat/indices?format=json", nil, &rows); err != nil {
		return nil, err
	}
	indices := make([]any, len(rows))
	for i, r := range rows {
		indices[i] = r
	}
	return map[string]any{"indices": indices}, nil
}

// InspectMapping returns the selected index's field mapping, so a user can see which
// fields exist and which types back a term pushdown. It needs an index selected.
func (s *Store) InspectMapping(ctx context.Context) (any, error) {
	if s.index == "" {
		return nil, errNoIndex
	}
	var raw map[string]json.RawMessage
	if err := s.request(ctx, "get mapping", http.MethodGet, "/"+s.index+"/_mapping", nil, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(raw))
	for name, m := range raw {
		var decoded any
		if err := json.Unmarshal(m, &decoded); err != nil {
			return nil, err
		}
		out[name] = decoded
	}
	return out, nil
}

// InspectAliases lists the server's aliases (the _cat/aliases table as JSON), so a
// user can see which aliases route to which indices. It needs no index.
func (s *Store) InspectAliases(ctx context.Context) (any, error) {
	var rows []map[string]any
	if err := s.request(ctx, "list aliases", http.MethodGet, "/_cat/aliases?format=json", nil, &rows); err != nil {
		return nil, err
	}
	aliases := make([]any, len(rows))
	for i, r := range rows {
		aliases[i] = r
	}
	return map[string]any{"aliases": aliases}, nil
}

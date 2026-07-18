package redis

import (
	"context"
	"errors"
	"fmt"

	goredis "github.com/redis/go-redis/v9"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/rawpred"
)

// The Redis store answers the optional filtered-scan port with a client-side
// raw-byte prefilter, so the engine prefers it over a plain ScanBatches.
var _ query.FilteredScanner = (*Store)(nil)

// ScanFiltered answers the query.FilteredScanner port: it walks the whole keyspace
// like ScanBatches but drops, before decoding, any RedisJSON document a pushed
// predicate provably cannot match. The predicate is a conservative superset and
// the engine re-runs the full jq over every returned page, so a client-side
// prefilter only shrinks how much this driver decodes and returns — it never
// changes results.
//
// The prefilter is byte-level and applies only to RedisJSON values, the sole Redis
// type that pays a JSON decode. Every other type (string, hash, list, set, zset,
// stream, missing) is normalized and returned exactly as ScanBatches does, so the
// engine's re-filter preserves the current mixed-keyspace semantics, including
// jq's behaviour on non-object values.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	// Prepare the predicate once for the whole scan: NewMatcher compiles every Regex
	// pattern here, so the per-document prefilter never recompiles one.
	matcher := rawpred.NewMatcher(pred)
	build := func(ctx context.Context, keys []string) (map[string]any, error) {
		return s.getFiltered(ctx, keys, matcher)
	}
	return s.scanPages(ctx, build, fn)
}

// getFiltered fetches a page like Get, but for RedisJSON keys it runs the raw
// JSON.GET reply through rawpred against pred and omits any key the predicate
// provably rejects, so a dropped document is never decoded. Every non-RedisJSON
// key normalizes and is included exactly as Get does — the engine re-filters those
// with the full jq. It costs the same two round-trips as Get regardless of key
// count.
func (s *Store) getFiltered(ctx context.Context, keys []string, matcher *rawpred.Matcher) (map[string]any, error) {
	unique := dedupe(keys)

	types, err := s.pipeTypes(ctx, unique)
	if err != nil {
		return nil, err
	}

	readers := make([]reader, len(unique))
	_, err = s.client.Pipelined(ctx, func(p goredis.Pipeliner) error {
		for i, k := range unique {
			r, rerr := filteredReaderFor(ctx, p, k, types[i], s.decimal, matcher)
			if rerr != nil {
				return rerr
			}
			readers[i] = r
		}
		return nil
	})
	// As in pipeValues, a per-command goredis.Nil surfaces here but is handled by
	// the per-reader normalize below, so only a transport error aborts.
	if err != nil && !errors.Is(err, goredis.Nil) {
		return nil, fmt.Errorf("redis read: %w", err)
	}

	out := make(map[string]any, len(unique))
	for i, k := range unique {
		v, err := readers[i].normalize()
		if err != nil {
			return nil, fmt.Errorf("read key %q: %w", k, err)
		}
		if _, drop := v.(dropped); drop {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// filteredReaderFor is readerFor with the RedisJSON case swapped for a
// predicate-aware reader; every other type uses the same reader as an unfiltered
// read, so mixed-keyspace normalization is unchanged.
func filteredReaderFor(ctx context.Context, p goredis.Pipeliner, key, typ string, dec numfmt.DecimalMode, matcher *rawpred.Matcher) (reader, error) {
	if typ == "ReJSON-RL" {
		return filterJSONReader{cmd: p.JSONGet(ctx, key), decimal: dec, matcher: matcher}, nil
	}
	return readerFor(ctx, p, key, typ, dec)
}

// dropped is the sentinel a filtered reader returns for a document the prefilter
// proved cannot match, so getFiltered omits the key without decoding it. It is
// unexported, so no normal value can collide with it.
type dropped struct{}

// filterJSONReader is jsonReader plus the prefilter: it runs the raw JSON.GET reply
// through rawpred and returns dropped on a provable non-match, otherwise decoding
// the survivor exactly as jsonReader does.
type filterJSONReader struct {
	cmd     *goredis.JSONCmd
	decimal numfmt.DecimalMode
	matcher *rawpred.Matcher
}

func (r filterJSONReader) normalize() (any, error) {
	s, err := r.cmd.Result()
	if err != nil {
		return nil, err
	}
	if r.matcher.Match([]byte(s)) == rawpred.CannotMatch {
		return dropped{}, nil
	}
	return decodeJSON(s, r.decimal)
}

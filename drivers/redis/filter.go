package redis

import (
	"context"
	"errors"

	goredis "github.com/redis/go-redis/v9"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/rawpred"
)

// The Redis store answers the optional filtered-scan port with a client-side
// raw-byte prefilter, so the engine prefers it over a plain ScanBatches.
var _ query.FilteredScanner = (*Store)(nil)

// ScanFiltered walks the whole keyspace like ScanBatches and drops RedisJSON
// documents that the predicate provably cannot match, before it decodes them.
// See query.FilteredScanner and rawpred.
//
// Only RedisJSON values are prefiltered, because they are the only Redis type
// that pays a JSON decode. Every other type is normalized and returned as
// ScanBatches returns it.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	matcher := rawpred.NewMatcher(pred)
	build := func(ctx context.Context, keys []string) (map[string]any, error) {
		return s.getFiltered(ctx, keys, matcher)
	}
	return s.scanPages(ctx, build, fn)
}

// getFiltered fetches a page like Get. For RedisJSON keys, it matches the raw
// JSON.GET reply with rawpred and omits each key that the predicate provably
// rejects. The engine re-filters the other keys. It costs the same two
// round-trips as Get for any key count.
func (s *Store) getFiltered(ctx context.Context, keys []string, matcher *rawpred.Matcher) (map[string]any, error) {
	unique := dedupe(keys)

	types, err := s.pipeTypes(ctx, unique)
	if err != nil {
		return nil, err
	}

	readers, err := s.readPipelined(ctx, unique, func(p goredis.Pipeliner, i int) (reader, error) {
		return filteredReaderFor(ctx, p, unique[i], types[i], s.decimal, matcher)
	})
	if err != nil {
		return nil, err
	}
	return normalizeAll(unique, readers)
}

// filteredReaderFor is readerFor with the RedisJSON case swapped for a
// predicate-aware reader; every other type uses the same reader as an unfiltered
// read, so mixed-keyspace normalization is unchanged.
func filteredReaderFor(ctx context.Context, p goredis.Pipeliner, key, typ string, dec numfmt.DecimalMode, matcher *rawpred.Matcher) (reader, error) {
	if typ == "ReJSON-RL" {
		return filterJSONReader{cmd: p.JSONGet(ctx, key), decimal: dec, matcher: matcher}, nil
	}
	return readerFor(ctx, p, typedKey{key: key, typ: typ}, dec)
}

// filterJSONReader is jsonReader plus the prefilter: it runs the raw JSON.GET reply
// through rawpred and reports a provable non-match as not-present, otherwise
// decoding the survivor exactly as jsonReader does. A dropped document and an
// absent key are the same thing to the caller — neither is in the map — so the
// presence flag carries both and no sentinel value is needed.
type filterJSONReader struct {
	cmd     *goredis.JSONCmd
	decimal numfmt.DecimalMode
	matcher *rawpred.Matcher
}

func (r filterJSONReader) normalize() (any, bool, error) {
	s, err := r.cmd.Result()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if r.matcher.Match([]byte(s)) == rawpred.CannotMatch {
		return nil, false, nil
	}
	v, err := decodeJSON(s, r.decimal)
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

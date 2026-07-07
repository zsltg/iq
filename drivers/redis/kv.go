package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/zsltg/iq/internal/numfmt"
)

// scanCount is the COUNT hint for each SCAN round; it bounds work per round-trip
// without changing the result, which is the full keyspace.
const scanCount = 100

// Get fetches each key and returns it normalized to JSON-ready Go values, keyed
// by key name. It reads the type of every key in one pipeline, then the value of
// every key with its type-appropriate reader in a second pipeline, so the whole
// batch costs two round-trips regardless of key count. A missing key maps to
// nil. Keys are read once each; duplicates in the input collapse.
func (s *Store) Get(ctx context.Context, keys []string) (map[string]any, error) {
	if len(keys) == 0 {
		return map[string]any{}, nil
	}
	unique := dedupe(keys)

	types, err := s.pipeTypes(ctx, unique)
	if err != nil {
		return nil, err
	}
	return s.pipeValues(ctx, unique, types)
}

// pipeTypes reads the Redis type of every key in a single pipeline.
func (s *Store) pipeTypes(ctx context.Context, keys []string) ([]string, error) {
	cmds := make([]*goredis.StatusCmd, len(keys))
	_, err := s.client.Pipelined(ctx, func(p goredis.Pipeliner) error {
		for i, k := range keys {
			cmds[i] = p.Type(ctx, k)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("redis type: %w", err)
	}
	types := make([]string, len(keys))
	for i, c := range cmds {
		types[i] = c.Val()
	}
	return types, nil
}

// pipeValues reads each key with the reader its type dictates, in a single
// pipeline, and normalizes each reply. An unsupported type fails fast rather
// than guessing an encoding.
func (s *Store) pipeValues(ctx context.Context, keys, types []string) (map[string]any, error) {
	readers := make([]reader, len(keys))
	_, err := s.client.Pipelined(ctx, func(p goredis.Pipeliner) error {
		for i, k := range keys {
			r, err := readerFor(ctx, p, k, types[i], s.decimal)
			if err != nil {
				return err
			}
			readers[i] = r
		}
		return nil
	})
	// A pipeline surfaces a per-command error (for example goredis.Nil) through
	// Pipelined's return; the per-reader normalize below handles those, so only a
	// transport error should abort here.
	if err != nil && !errors.Is(err, goredis.Nil) {
		return nil, fmt.Errorf("redis read: %w", err)
	}

	out := make(map[string]any, len(keys))
	for i, k := range keys {
		v, err := readers[i].normalize()
		if err != nil {
			return nil, fmt.Errorf("read key %q: %w", k, err)
		}
		out[k] = v
	}
	return out, nil
}

// reader normalizes one key's pipelined reply into a JSON-ready value.
type reader interface {
	normalize() (any, error)
}

// readerFor queues the read appropriate to a key's type and returns the reader
// that will normalize the reply once the pipeline executes.
func readerFor(ctx context.Context, p goredis.Pipeliner, key, typ string, dec numfmt.DecimalMode) (reader, error) {
	switch typ {
	case "none":
		return missingReader{}, nil
	case "string":
		return stringReader{p.Get(ctx, key)}, nil
	case "hash":
		return hashReader{p.HGetAll(ctx, key)}, nil
	case "list":
		return listReader{p.LRange(ctx, key, 0, -1)}, nil
	case "set":
		return setReader{p.SMembers(ctx, key)}, nil
	case "zset":
		return zsetReader{p.ZRangeWithScores(ctx, key, 0, -1)}, nil
	case "stream":
		return streamReader{p.XRange(ctx, key, "-", "+")}, nil
	case "ReJSON-RL":
		return jsonReader{cmd: p.JSONGet(ctx, key), decimal: dec}, nil
	default:
		// Other module types (time series, bloom, and so on) have no frozen JSON
		// encoding yet, so refuse rather than emit a lossy or ambiguous value.
		return nil, fmt.Errorf("unsupported redis type %q for key %q", typ, key)
	}
}

// missingReader normalizes an absent key to null.
type missingReader struct{}

func (missingReader) normalize() (any, error) { return nil, nil }

// stringReader normalizes a string value. The raw string is preserved; numeric
// strings are not coerced, so a jq filter decides whether to `tonumber`.
type stringReader struct{ cmd *goredis.StringCmd }

func (r stringReader) normalize() (any, error) {
	v, err := r.cmd.Result()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// hashReader normalizes a hash to an object of string fields.
type hashReader struct{ cmd *goredis.MapStringStringCmd }

func (r hashReader) normalize() (any, error) {
	m, err := r.cmd.Result()
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out, nil
}

// listReader normalizes a list to an array in list order.
type listReader struct{ cmd *goredis.StringSliceCmd }

func (r listReader) normalize() (any, error) {
	vs, err := r.cmd.Result()
	if err != nil {
		return nil, err
	}
	return toAnySlice(vs), nil
}

// setReader normalizes a set to a lexically sorted array. A Redis set has no
// order, so sorting makes the output deterministic and testable.
type setReader struct{ cmd *goredis.StringSliceCmd }

func (r setReader) normalize() (any, error) {
	vs, err := r.cmd.Result()
	if err != nil {
		return nil, err
	}
	sort.Strings(vs)
	return toAnySlice(vs), nil
}

// zsetReader normalizes a sorted set to an array of {member, score} objects in
// score-ascending order, preserving rank — the frozen canonical encoding.
type zsetReader struct{ cmd *goredis.ZSliceCmd }

func (r zsetReader) normalize() (any, error) {
	zs, err := r.cmd.Result()
	if err != nil {
		return nil, err
	}
	out := make([]any, len(zs))
	for i, z := range zs {
		member, _ := z.Member.(string)
		out[i] = map[string]any{"member": member, "score": z.Score}
	}
	return out, nil
}

// streamReader normalizes a stream to an array of {id, fields} objects in entry
// order. Fields are an object, matching the hash encoding; go-redis already
// returns an entry's fields as a map, so field order and any duplicate field
// names are not available to preserve.
type streamReader struct{ cmd *goredis.XMessageSliceCmd }

func (r streamReader) normalize() (any, error) {
	msgs, err := r.cmd.Result()
	if err != nil {
		return nil, err
	}
	out := make([]any, len(msgs))
	for i, m := range msgs {
		fields := make(map[string]any, len(m.Values))
		for k, v := range m.Values {
			fields[k] = v
		}
		out[i] = map[string]any{"id": m.ID, "fields": fields}
	}
	return out, nil
}

// jsonReader normalizes a RedisJSON document by parsing its JSON.GET reply. The
// stored value is already JSON; decimal chooses how a fractional number is
// presented to the filter.
type jsonReader struct {
	cmd     *goredis.JSONCmd
	decimal numfmt.DecimalMode
}

func (r jsonReader) normalize() (any, error) {
	s, err := r.cmd.Result()
	if err != nil {
		return nil, err
	}
	return decodeJSON(s, r.decimal)
}

// decodeJSON parses a JSON document into JSON-ready Go values, decoding numbers
// deliberately (UseNumber) so precision survives: an integer becomes an exact int
// or *big.Int, and a fractional number follows the decimal mode. Plain
// json.Unmarshal would collapse every number to a float64 and silently lose
// precision beyond 2^53.
func decodeJSON(s string, mode numfmt.DecimalMode) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decode redis json: %w", err)
	}
	return numfmt.ConvertNumbers(v, mode), nil
}

// ScanBatches walks the keyspace with a cursor — never blocking the server the
// way KEYS would — fetching the values of each page and handing them to fn as
// {key: value}. A streaming caller keeps only one page in memory. SCAN may
// return a key more than once if the keyspace is resized mid-scan, so a page may
// repeat a key; the caller opts into that trade for bounded memory. The walk is
// bounded by ctx and stops at the first error from fn or the store.
func (s *Store) ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error {
	iter := s.client.Scan(ctx, 0, "*", scanCount).Iterator()
	page := make([]string, 0, scanCount)
	flush := func() error {
		if len(page) == 0 {
			return nil
		}
		batch, err := s.Get(ctx, page)
		if err != nil {
			return err
		}
		page = page[:0]
		return fn(batch)
	}
	for iter.Next(ctx) {
		page = append(page, iter.Val())
		if len(page) >= s.pageSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("redis scan: %w", err)
	}
	return flush()
}

// FormatRaw renders a raw command reply in redis-cli's cooked style. It lets the
// Redis store satisfy the CLI's backend interface alongside the MongoDB store,
// which formats its raw replies as JSON instead.
func (s *Store) FormatRaw(v any, colored bool) string {
	return FormatReply(v, colored)
}

// dedupe returns keys with duplicates removed, preserving first-seen order so a
// key is read exactly once.
func dedupe(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// toAnySlice widens a string slice to []any so it marshals as a JSON array.
func toAnySlice(vs []string) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

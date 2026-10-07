package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"strconv"

	goredis "github.com/redis/go-redis/v9"

	"github.com/zsltg/iq/internal/query"
)

// Put writes a batch of records back into Redis, reconstructing each key's native
// structure from its type tag — the inverse of the typed readers in kv.go. Upsert
// replaces the whole value: an aggregate is DEL'd before it is rewritten, so a
// list/set/hash is replaced rather than appended into. InsertOnly writes only keys
// that do not yet exist. Every argument is passed discretely through the pipeline,
// never concatenated into a command string.
func (s *Store) Put(ctx context.Context, batch []query.Record, mode query.WriteMode) (query.WriteStat, error) {
	if len(batch) == 0 {
		return query.WriteStat{}, nil
	}
	if mode == query.InsertOnly {
		return s.putInsertOnly(ctx, batch)
	}
	return s.putUpsert(ctx, batch)
}

// putUpsert deletes then rewrites each key in one pipeline. The DEL reply doubles
// as the overwrite counter: a return of 1 means the key existed (overwritten), 0
// that it is new (written).
func (s *Store) putUpsert(ctx context.Context, batch []query.Record) (query.WriteStat, error) {
	dels := make([]*goredis.IntCmd, len(batch))
	_, err := s.client.Pipelined(ctx, func(p goredis.Pipeliner) error {
		for i, r := range batch {
			del, err := queueReplace(ctx, p, r)
			if err != nil {
				return err
			}
			dels[i] = del
		}
		return nil
	})
	if err != nil {
		return query.WriteStat{}, fmt.Errorf("redis write: %w", err)
	}
	return countReplaced(dels), nil
}

// queueReplace queues the DEL and the write commands that replace one record, and
// returns the DEL reply. The reply tells the caller whether the key existed.
func queueReplace(ctx context.Context, p goredis.Pipeliner, r query.Record) (*goredis.IntCmd, error) {
	if err := requireKey(r); err != nil {
		return nil, err
	}
	del := p.Del(ctx, r.Key)
	if err := queueWrite(ctx, p, r); err != nil {
		return nil, err
	}
	return del, nil
}

// requireKey rejects a record that has no key, before any command is queued.
func requireKey(r query.Record) error {
	if r.Key == "" {
		return fmt.Errorf("%w; use --key-field for foreign input", query.ErrNoKey)
	}
	return nil
}

// countReplaced turns the DEL replies of an upsert into a WriteStat: a reply above
// zero is an overwrite, anything else is a new key.
func countReplaced(dels []*goredis.IntCmd) query.WriteStat {
	var stat query.WriteStat
	for _, d := range dels {
		if d.Val() > 0 {
			stat.Overwritten++
		} else {
			stat.Written++
		}
	}
	return stat
}

// putInsertOnly skips keys that already exist. It reads existence in one pipeline,
// then writes only the absent keys in a second, so an existing key is never
// clobbered.
func (s *Store) putInsertOnly(ctx context.Context, batch []query.Record) (query.WriteStat, error) {
	exists, err := s.pipeExists(ctx, batch)
	if err != nil {
		return query.WriteStat{}, err
	}
	return s.writeAbsent(ctx, batch, exists)
}

// pipeExists reads, in one pipeline, whether each record's key exists.
func (s *Store) pipeExists(ctx context.Context, batch []query.Record) ([]*goredis.IntCmd, error) {
	exists := make([]*goredis.IntCmd, len(batch))
	_, err := s.client.Pipelined(ctx, func(p goredis.Pipeliner) error {
		for i, r := range batch {
			if err := requireKey(r); err != nil {
				return err
			}
			exists[i] = p.Exists(ctx, r.Key)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("redis exists: %w", err)
	}
	return exists, nil
}

// writeAbsent writes, in one pipeline, the records whose EXISTS reply is zero. It
// counts the records that it wrote and the records that it skipped.
func (s *Store) writeAbsent(ctx context.Context, batch []query.Record, exists []*goredis.IntCmd) (query.WriteStat, error) {
	var stat query.WriteStat
	_, err := s.client.Pipelined(ctx, func(p goredis.Pipeliner) error {
		for i, r := range batch {
			if exists[i].Val() > 0 {
				stat.Skipped++
				continue
			}
			if err := queueWrite(ctx, p, r); err != nil {
				return err
			}
			stat.Written++
		}
		return nil
	})
	if err != nil {
		return query.WriteStat{}, fmt.Errorf("redis write: %w", err)
	}
	return stat, nil
}

// queueWrite queues the command(s) that reconstruct one record's value, chosen by
// its resolved type. It fails fast — before the pipeline executes — on a type it
// cannot write or a value whose shape does not match its type, so a bad record
// never half-writes.
func queueWrite(ctx context.Context, p goredis.Pipeliner, r query.Record) error {
	switch resolveType(r) {
	case "string":
		return queueString(ctx, p, r)
	case "hash":
		return queueHash(ctx, p, r)
	case "list":
		return queueElements(r, func(key string, vals []any) { p.RPush(ctx, key, vals...) })
	case "set":
		return queueElements(r, func(key string, vals []any) { p.SAdd(ctx, key, vals...) })
	case "zset":
		return queueZSet(ctx, p, r)
	case "stream":
		return queueStream(ctx, p, r)
	case "json":
		return queueJSON(ctx, p, r)
	default:
		return fmt.Errorf("key %q: unsupported redis type %q", r.Key, r.Type)
	}
}

// queueString queues a SET that replaces the string value and drops any expiry.
func queueString(ctx context.Context, p goredis.Pipeliner, r query.Record) error {
	v, err := redisString(r.Value)
	if err != nil {
		return fmt.Errorf("key %q: %w", r.Key, err)
	}
	p.Set(ctx, r.Key, v, 0)
	return nil
}

// queueJSON queues a JSON.SET at the document root.
func queueJSON(ctx context.Context, p goredis.Pipeliner, r query.Record) error {
	raw, err := json.Marshal(r.Value)
	if err != nil {
		return fmt.Errorf("key %q: encode json: %w", r.Key, err)
	}
	p.Do(ctx, "JSON.SET", r.Key, "$", string(raw))
	return nil
}

// resolveType picks the Redis type to write a record as: its tag when set, else
// inferred from the value's shape — a scalar as a string, an object or array as
// JSON. A reshaping copy leaves the tag empty, so this is where an untyped value
// lands on a concrete Redis type.
func resolveType(r query.Record) string {
	if r.Type != "" {
		if r.Type == "document" {
			return "json" // a Mongo document has no native Redis type; store it as JSON.
		}
		return r.Type
	}
	switch r.Value.(type) {
	case map[string]any, []any:
		return "json"
	default:
		return "string"
	}
}

// queueHash queues an HSET reconstructing a hash from its normalized object. A
// non-object value, or a field value that is not a scalar, is a clear error.
func queueHash(ctx context.Context, p goredis.Pipeliner, r query.Record) error {
	obj, ok := r.Value.(map[string]any)
	if !ok {
		return fmt.Errorf("key %q: hash value is not an object", r.Key)
	}
	if len(obj) == 0 {
		return nil // Redis has no empty hash; nothing to write.
	}
	fields := make([]any, 0, len(obj))
	for k, v := range obj {
		s, err := redisString(v)
		if err != nil {
			return fmt.Errorf("key %q field %q: %w", r.Key, k, err)
		}
		fields = append(fields, k, s)
	}
	p.HSet(ctx, r.Key, fields...)
	return nil
}

// queueElements queues a list or set reconstruction from a normalized array via
// add, which appends the stringified elements under key. The pipeline and its
// context reach the server through add alone, so this stringifying half needs
// neither.
func queueElements(r query.Record, add func(key string, vals []any)) error {
	arr, ok := r.Value.([]any)
	if !ok {
		return fmt.Errorf("key %q: value is not an array", r.Key)
	}
	if len(arr) == 0 {
		return nil // Redis has no empty list/set; nothing to write.
	}
	vals := make([]any, len(arr))
	for i, e := range arr {
		s, err := redisString(e)
		if err != nil {
			return fmt.Errorf("key %q: %w", r.Key, err)
		}
		vals[i] = s
	}
	add(r.Key, vals)
	return nil
}

// queueZSet queues a ZADD reconstructing a sorted set from its normalized
// [{member, score}] array.
func queueZSet(ctx context.Context, p goredis.Pipeliner, r query.Record) error {
	arr, ok := r.Value.([]any)
	if !ok {
		return fmt.Errorf("key %q: zset value is not an array", r.Key)
	}
	if len(arr) == 0 {
		return nil
	}
	members := make([]goredis.Z, len(arr))
	for i, e := range arr {
		z, err := zsetMember(r.Key, e)
		if err != nil {
			return err
		}
		members[i] = z
	}
	p.ZAdd(ctx, r.Key, members...)
	return nil
}

// zsetMember converts one normalized {member, score} element to a goredis.Z.
func zsetMember(key string, e any) (goredis.Z, error) {
	obj, ok := e.(map[string]any)
	if !ok {
		return goredis.Z{}, fmt.Errorf("key %q: zset element is not a {member, score} object", key)
	}
	member, err := redisString(obj["member"])
	if err != nil {
		return goredis.Z{}, fmt.Errorf("key %q: zset member: %w", key, err)
	}
	score, err := asFloat(obj["score"])
	if err != nil {
		return goredis.Z{}, fmt.Errorf("key %q: zset score: %w", key, err)
	}
	return goredis.Z{Score: score, Member: member}, nil
}

// queueStream queues an XADD per entry, reconstructing a stream from its
// normalized [{id, fields}] array in entry order.
func queueStream(ctx context.Context, p goredis.Pipeliner, r query.Record) error {
	arr, ok := r.Value.([]any)
	if !ok {
		return fmt.Errorf("key %q: stream value is not an array", r.Key)
	}
	for _, e := range arr {
		if err := queueStreamEntry(ctx, p, r.Key, e); err != nil {
			return err
		}
	}
	return nil
}

// queueStreamEntry queues one XADD for a normalized {id, fields} element.
func queueStreamEntry(ctx context.Context, p goredis.Pipeliner, key string, e any) error {
	entry, ok := e.(map[string]any)
	if !ok {
		return fmt.Errorf("key %q: stream entry is not an {id, fields} object", key)
	}
	id, err := redisString(entry["id"])
	if err != nil {
		return fmt.Errorf("key %q: stream id: %w", key, err)
	}
	rawFields, ok := entry["fields"].(map[string]any)
	if !ok {
		return fmt.Errorf("key %q: stream fields is not an object", key)
	}
	values, err := streamFields(key, rawFields)
	if err != nil {
		return err
	}
	p.XAdd(ctx, &goredis.XAddArgs{Stream: key, ID: id, Values: values})
	return nil
}

// streamFields converts the fields of a stream entry to strings.
func streamFields(key string, raw map[string]any) (map[string]any, error) {
	values := make(map[string]any, len(raw))
	for k, v := range raw {
		s, err := redisString(v)
		if err != nil {
			return nil, fmt.Errorf("key %q field %q: %w", key, k, err)
		}
		values[k] = s
	}
	return values, nil
}

// redisString renders a scalar as the string Redis stores. A composite value
// (object or array) has no scalar form and is a clear error, so a shape mismatch
// never silently stringifies to "map[...]".
func redisString(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case *big.Int:
		return t.String(), nil
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(t), nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("value %T is not a scalar", v)
	}
}

// asFloat coerces a normalized number to the float64 a zset score needs.
func asFloat(v any) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case int:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case *big.Int:
		f, _ := new(big.Float).SetInt(t).Float64()
		return f, nil
	default:
		return 0, fmt.Errorf("score %T is not a number", v)
	}
}

// Clear empties the connected Redis logical database with FLUSHDB — the `iq data
// clear` semantics for a keyspace. Redis has no removable container (the DB index
// always exists), so the Store implements Clearer but not Dropper; `iq data drop`
// rejects a Redis target through the absent capability.
func (s *Store) Clear(ctx context.Context) error {
	if err := s.client.FlushDB(ctx).Err(); err != nil {
		return fmt.Errorf("redis flushdb: %w", err)
	}
	return nil
}

// Delete removes the named keys with DEL, chunked to pageSize so a large list is
// bounded per round-trip. The DEL reply is the count of keys that actually existed,
// so Deleted is exact and Missing is the remainder — no pre-read needed. Each key is
// passed discretely as a command argument, never concatenated.
func (s *Store) Delete(ctx context.Context, keys []string) (query.DeleteStat, error) {
	var stat query.DeleteStat
	for chunk := range slices.Chunk(keys, s.pageSize) {
		n, err := s.client.Del(ctx, chunk...).Result()
		if err != nil {
			return query.DeleteStat{}, fmt.Errorf("redis del: %w", err)
		}
		stat.Deleted += int(n)
		stat.Missing += len(chunk) - int(n)
	}
	return stat, nil
}

// TypedScan streams the whole keyspace as typed records, so a copy reconstructs
// each key's native structure. It mirrors ScanBatches but carries each key's TYPE
// alongside its normalized value.
func (s *Store) TypedScan(ctx context.Context, fn func(batch []query.Record) error) error {
	return s.walkKeyPages(ctx, func(page []string) error {
		recs, err := s.typedGet(ctx, page)
		if err != nil {
			return err
		}
		return fn(recs)
	})
}

// typedGet reads a page of keys into typed records, reusing the read pipelines but
// keeping each key's type so the value can later be reconstructed exactly. The
// module type "ReJSON-RL" is surfaced as the neutral tag "json".
func (s *Store) typedGet(ctx context.Context, keys []string) ([]query.Record, error) {
	unique := dedupe(keys)
	types, err := s.pipeTypes(ctx, unique)
	if err != nil {
		return nil, err
	}
	values, err := s.pipeValues(ctx, unique, types)
	if err != nil {
		return nil, err
	}
	recs := make([]query.Record, 0, len(unique))
	for i, k := range unique {
		// A key that vanished between SCAN and read is absent from values, whether
		// TYPE already said so or the value read raced; skip it rather than write
		// a null. A stored JSON null is present, so it still becomes a record.
		v, ok := values[k]
		if !ok {
			continue
		}
		recs = append(recs, query.Record{Key: k, Type: typeName(types[i]), Value: v})
	}
	return recs, nil
}

// typeName maps a Redis TYPE reply to the neutral type tag a record carries,
// collapsing the RedisJSON module type to "json".
func typeName(t string) string {
	if t == "ReJSON-RL" {
		return "json"
	}
	return t
}

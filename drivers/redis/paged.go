package redis

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// maxKeyElements is the most elements that one hash, list, set, sorted set or
// stream can hold and still be read. A bigger key needs the unbounded opt-in
// (query.Unbounded). Strings and RedisJSON values have no element count, so the
// limit does not apply to them.
const maxKeyElements = 1_000_000

// readChunk is how many elements one read command asks for. A key with more
// elements than this takes more than one round trip. This keeps one huge reply
// from blocking the server.
const readChunk = 1000

// ErrKeyTooLarge reports a key with more elements than maxKeyElements, read
// without the unbounded opt-in.
var ErrKeyTooLarge = errors.New("too many elements")

// limits holds the size limits of a read. An opened Store uses the constants
// above. A test lowers them to cover paging without a million elements.
type limits struct {
	maxElements int64
	chunk       int64
}

// defaultLimits returns the limits of an opened Store.
func defaultLimits() limits {
	return limits{maxElements: maxKeyElements, chunk: readChunk}
}

// pagedRead is the part that the readers of the five element types share. The
// pipeline round trip holds the size command and the first chunk of the key.
// The reader reads the next chunks itself, with rd, only when the key needs them.
type pagedRead struct {
	ctx    context.Context //nolint:containedctx // the reader lives for one read and sends its follow-up commands with this context.
	rd     goredis.Cmdable
	key    string
	typ    string
	size   *goredis.IntCmd
	limits limits
}

// paged builds the shared part for key k, whose size command size is queued in the
// same pipeline as the first chunk.
func (s *Store) paged(ctx context.Context, k typedKey, size *goredis.IntCmd) pagedRead {
	return pagedRead{ctx: ctx, rd: s.client, key: k.key, typ: k.typ, size: size, limits: s.limits}
}

// admit refuses a key that is over the element limit, unless the context carries
// the unbounded opt-in. The error gives the type and the size, never a value.
func (r pagedRead) admit() error {
	n, err := r.size.Result()
	if err != nil {
		return err
	}
	if n > r.limits.maxElements && !query.Unbounded(r.ctx) {
		return fmt.Errorf("%w: the %s has %d elements and the limit is %d, so read it with --unbounded",
			ErrKeyTooLarge, r.typ, n, r.limits.maxElements)
	}
	return nil
}

// scanAll adds the pairs or members of every SCAN-family round to add. It starts
// with the reply of the first round, which the pipeline already read, and then
// asks next for each later cursor until the cursor is 0.
func scanAll(first *goredis.ScanCmd, next func(cursor uint64) *goredis.ScanCmd, add func(flat []string) error) error {
	flat, cursor, err := first.Result()
	for err == nil {
		if err = add(flat); err != nil || cursor == 0 {
			return err
		}
		flat, cursor, err = next(cursor).Result()
	}
	return err
}

// hashReader normalizes a hash to an object of string fields. HSCAN can return a
// field more than once, so the object keeps the last value that it saw.
type hashReader struct {
	pagedRead
	first *goredis.ScanCmd
}

func (r hashReader) normalize() (any, bool, error) {
	if err := r.admit(); err != nil {
		return nil, false, err
	}
	out := map[string]any{}
	next := func(c uint64) *goredis.ScanCmd { return r.rd.HScan(r.ctx, r.key, c, "", r.limits.chunk) }
	err := scanAll(r.first, next, func(flat []string) error {
		return addPairs(flat, func(field, value string) { out[field] = value })
	})
	if err != nil {
		return nil, false, err
	}
	// Redis deletes a hash when its last field goes, so an empty reply means the
	// key vanished after TYPE saw it. The key is absent, not an
	// empty object.
	if len(out) == 0 {
		return nil, false, nil
	}
	return out, true, nil
}

// addPairs hands each (name, value) pair of a flat HSCAN or ZSCAN reply to set.
func addPairs(flat []string, set func(name, value string)) error {
	if len(flat)%2 != 0 {
		return fmt.Errorf("scan reply has %d items, expected pairs", len(flat))
	}
	for i := 0; i < len(flat); i += 2 {
		set(flat[i], flat[i+1])
	}
	return nil
}

// listReader normalizes a list to an array in list order. It reads the list in
// chunks of LRANGE.
type listReader struct {
	pagedRead
	first *goredis.StringSliceCmd
}

func (r listReader) normalize() (any, bool, error) {
	if err := r.admit(); err != nil {
		return nil, false, err
	}
	out, err := r.first.Result()
	if err != nil {
		return nil, false, err
	}
	for chunk := out; int64(len(chunk)) >= r.limits.chunk; {
		start := int64(len(out))
		chunk, err = r.rd.LRange(r.ctx, r.key, start, start+r.limits.chunk-1).Result()
		if err != nil {
			return nil, false, err
		}
		out = append(out, chunk...)
	}
	// Redis deletes a list when its last element goes, so an empty reply means
	// the key vanished after TYPE saw it.
	if len(out) == 0 {
		return nil, false, nil
	}
	return toAnySlice(out), true, nil
}

// setReader normalizes a set to a lexically sorted array. A Redis set has no
// order, so sorting makes the output deterministic and testable. SSCAN can return
// a member more than once, so the reader drops the repeats before it sorts.
type setReader struct {
	pagedRead
	first *goredis.ScanCmd
}

func (r setReader) normalize() (any, bool, error) {
	if err := r.admit(); err != nil {
		return nil, false, err
	}
	seen := map[string]struct{}{}
	next := func(c uint64) *goredis.ScanCmd { return r.rd.SScan(r.ctx, r.key, c, "", r.limits.chunk) }
	err := scanAll(r.first, next, func(flat []string) error {
		for _, m := range flat {
			seen[m] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if len(seen) == 0 {
		return nil, false, nil
	}
	members := slices.Sorted(maps.Keys(seen))
	return toAnySlice(members), true, nil
}

// zsetReader normalizes a sorted set to an array of {member, score} objects in
// score-ascending order, preserving rank. This is the frozen canonical
// encoding. ZSCAN has no order and can return a member more than once. So the
// reader drops the repeats, then sorts by score and by member, which is the
// order of ZRANGE. A NaN or infinite score has no JSON form, so it becomes a
// string (see numfmt.FiniteScore). The reader reports it to the notice sink of
// ctx, under key.
type zsetReader struct {
	pagedRead
	first *goredis.ScanCmd
}

func (r zsetReader) normalize() (any, bool, error) {
	if err := r.admit(); err != nil {
		return nil, false, err
	}
	scores := map[string]float64{}
	next := func(c uint64) *goredis.ScanCmd { return r.rd.ZScan(r.ctx, r.key, c, "", r.limits.chunk) }
	err := scanAll(r.first, next, func(flat []string) error {
		return addScores(flat, scores)
	})
	if err != nil {
		return nil, false, err
	}
	// An empty sorted set cannot exist in Redis. The key went away after TYPE.
	if len(scores) == 0 {
		return nil, false, nil
	}
	return zsetValue(r.ctx, r.key, sortedZ(scores)), true, nil
}

// addScores parses the (member, score) pairs of a ZSCAN reply into scores.
func addScores(flat []string, scores map[string]float64) error {
	var bad error
	err := addPairs(flat, func(member, score string) {
		f, perr := strconv.ParseFloat(score, 64)
		if perr != nil {
			bad = fmt.Errorf("parse score of a sorted set member: %w", perr)
			return
		}
		scores[member] = f
	})
	return cmp.Or(err, bad)
}

// sortedZ returns the members in ZRANGE order: score ascending, and for equal
// scores the member in byte order.
func sortedZ(scores map[string]float64) []goredis.Z {
	zs := make([]goredis.Z, 0, len(scores))
	for m, s := range scores {
		zs = append(zs, goredis.Z{Member: m, Score: s})
	}
	slices.SortFunc(zs, func(a, b goredis.Z) int {
		return cmp.Or(cmp.Compare(a.Score, b.Score), strings.Compare(a.Member.(string), b.Member.(string)))
	})
	return zs
}

// zsetValue builds the frozen {member, score} array. It reports each score that
// is not finite to the notice sink of ctx.
func zsetValue(ctx context.Context, key string, zs []goredis.Z) []any {
	h := &numfmt.Hits{}
	out := make([]any, len(zs))
	for i, z := range zs {
		member, _ := z.Member.(string)
		h.EnterIndex(i)
		h.Enter("score")
		out[i] = map[string]any{"member": member, "score": h.Score(z.Score)}
		h.Leave()
		h.Leave()
	}
	query.NoteNonFinite(ctx, key, h.Fields...)
	return out
}

// streamReader normalizes a stream to an array of {id, fields} objects in entry
// order. Fields are an object, matching the hash encoding. go-redis already
// returns the fields of an entry as a map. So the field order and any
// duplicate field names are not available to preserve. It reads the stream in
// chunks of XRANGE with COUNT.
type streamReader struct {
	pagedRead
	first *goredis.XMessageSliceCmd
}

func (r streamReader) normalize() (any, bool, error) {
	if err := r.admit(); err != nil {
		return nil, false, err
	}
	msgs, err := r.first.Result()
	if err != nil {
		return nil, false, err
	}
	for chunk := msgs; int64(len(chunk)) >= r.limits.chunk; {
		chunk, err = r.nextEntries(chunk[len(chunk)-1].ID)
		if err != nil {
			return nil, false, err
		}
		msgs = append(msgs, chunk...)
	}
	out := make([]any, len(msgs))
	for i, m := range msgs {
		fields := make(map[string]any, len(m.Values))
		maps.Copy(fields, m.Values)
		out[i] = map[string]any{"id": m.ID, "fields": fields}
	}
	// Unlike the other collections a stream survives losing every entry, so an
	// empty reply is a present, empty stream.
	return out, true, nil
}

// nextEntries reads the chunk of entries that follows the entry with ID last.
func (r streamReader) nextEntries(last string) ([]goredis.XMessage, error) {
	start, err := nextStreamID(last)
	if err != nil {
		return nil, err
	}
	return r.rd.XRangeN(r.ctx, r.key, start, "+", r.limits.chunk).Result()
}

// nextStreamID returns the smallest stream ID that is greater than id. It does
// this with arithmetic and not with the exclusive "(" range of XRANGE, which
// older servers do not support.
func nextStreamID(id string) (string, error) {
	sid, err := parseStreamID(id)
	if err != nil {
		return "", err
	}
	if sid.seq == math.MaxUint64 {
		sid.ms++
		sid.seq = 0
	} else {
		sid.seq++
	}
	return strconv.FormatUint(sid.ms, 10) + "-" + strconv.FormatUint(sid.seq, 10), nil
}

// streamID holds the two numbers of a stream entry ID.
type streamID struct{ ms, seq uint64 }

// parseStreamID splits a stream entry ID of the form <ms>-<seq> into its two numbers.
// An ID without "-" has an empty seq text, which fails to parse like any other
// bad number.
func parseStreamID(id string) (streamID, error) {
	msText, seqText, _ := strings.Cut(id, "-")
	ms, msErr := strconv.ParseUint(msText, 10, 64)
	seq, seqErr := strconv.ParseUint(seqText, 10, 64)
	if msErr != nil || seqErr != nil {
		return streamID{}, fmt.Errorf("stream entry ID %q is not <ms>-<seq>", id)
	}
	return streamID{ms: ms, seq: seq}, nil
}

// nativeReader queues the read for a built-in Redis type (string, hash, list,
// set, zset, stream) and returns its reader. A type with elements also queues its
// size command, in the same pipeline.
func (s *Store) nativeReader(ctx context.Context, p goredis.Pipeliner, k typedKey) (reader, error) {
	chunk := s.limits.chunk
	switch k.typ {
	case "string":
		return stringReader{p.Get(ctx, k.key)}, nil
	case "hash":
		return hashReader{s.paged(ctx, k, p.HLen(ctx, k.key)), p.HScan(ctx, k.key, 0, "", chunk)}, nil
	case "list":
		return listReader{s.paged(ctx, k, p.LLen(ctx, k.key)), p.LRange(ctx, k.key, 0, chunk-1)}, nil
	case "set":
		return setReader{s.paged(ctx, k, p.SCard(ctx, k.key)), p.SScan(ctx, k.key, 0, "", chunk)}, nil
	case "zset":
		return zsetReader{s.paged(ctx, k, p.ZCard(ctx, k.key)), p.ZScan(ctx, k.key, 0, "", chunk)}, nil
	case "stream":
		return streamReader{s.paged(ctx, k, p.XLen(ctx, k.key)), p.XRangeN(ctx, k.key, "-", "+", chunk)}, nil
	default:
		// Other module types (time series, bloom, and so on) have no frozen JSON
		// encoding yet, so refuse rather than emit a lossy or ambiguous value.
		return nil, fmt.Errorf("unsupported redis type %q for key %q", k.typ, k.key)
	}
}

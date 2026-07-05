package file

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/hdt3213/rdb/parser"

	"github.com/zsltg/iq/internal/query"
)

// rdbSource streams a Redis RDB snapshot as typed records. Each object is
// converted to the exact Type/Value shape the live Redis adapter's typed read
// produces (see drivers/redis: string→string, hash→object, list/set→array,
// zset→[{member,score}], stream→[{id,fields}]), so restoring an RDB round-trips
// losslessly and a jq query over a dump matches a query over the live server. RDB
// metadata (aux, resizedb, functions) and opaque module types carry no keyspace
// value and are skipped.
func rdbSource(r io.Reader, size int) query.RecordSource {
	return func(ctx context.Context, fn func(batch []query.Record) error) error {
		page := make([]query.Record, 0, size)
		var cbErr error
		parseErr := parser.NewDecoder(r).Parse(func(o parser.RedisObject) bool {
			if err := ctx.Err(); err != nil {
				cbErr = err
				return false
			}
			rec, ok := recordForObject(o)
			if !ok {
				return true
			}
			page = append(page, rec)
			if len(page) >= size {
				if err := fn(page); err != nil {
					cbErr = err
					return false
				}
				page = page[:0]
			}
			return true
		})
		if cbErr != nil {
			return cbErr
		}
		if parseErr != nil {
			return fmt.Errorf("parse rdb: %w", parseErr)
		}
		if len(page) > 0 {
			return fn(page)
		}
		return nil
	}
}

// recordForObject converts one RDB object to a typed record, reporting false for an
// object that carries no keyspace value (metadata or an unsupported module type).
func recordForObject(o parser.RedisObject) (query.Record, bool) {
	key := o.GetKey()
	switch obj := o.(type) {
	case *parser.StringObject:
		return query.Record{Key: key, Type: "string", Value: string(obj.Value)}, true
	case *parser.ListObject:
		return query.Record{Key: key, Type: "list", Value: bytesToAny(obj.Values)}, true
	case *parser.SetObject:
		members := bytesToStrings(obj.Members)
		sort.Strings(members) // a set has no order; the live adapter sorts lexically.
		return query.Record{Key: key, Type: "set", Value: stringsToAny(members)}, true
	case *parser.HashObject:
		m := make(map[string]any, len(obj.Hash))
		for k, v := range obj.Hash {
			m[k] = string(v)
		}
		return query.Record{Key: key, Type: "hash", Value: m}, true
	case *parser.ZSetObject:
		return query.Record{Key: key, Type: "zset", Value: zsetValue(obj)}, true
	case *parser.StreamObject:
		return query.Record{Key: key, Type: "stream", Value: streamValue(obj)}, true
	default:
		return query.Record{}, false
	}
}

// zsetValue renders a sorted set as [{member, score}] in score-ascending order,
// ties broken lexically by member — the order ZRANGEWITHSCORES returns, which the
// live adapter's canonical encoding preserves.
func zsetValue(obj *parser.ZSetObject) []any {
	// The parsed object is single-use, so sorting its entries in place is safe and
	// avoids naming the (non-re-exported) entry type.
	sort.SliceStable(obj.Entries, func(i, j int) bool {
		if obj.Entries[i].Score != obj.Entries[j].Score {
			return obj.Entries[i].Score < obj.Entries[j].Score
		}
		return obj.Entries[i].Member < obj.Entries[j].Member
	})
	out := make([]any, len(obj.Entries))
	for i, e := range obj.Entries {
		out[i] = map[string]any{"member": e.Member, "score": e.Score}
	}
	return out
}

// streamValue renders a stream as [{id, fields}] in id order, skipping tombstoned
// messages — matching XRANGE, the live adapter's encoding. Consumer groups are
// stream metadata the record model does not carry.
func streamValue(obj *parser.StreamObject) []any {
	type msg struct {
		ms, seq uint64
		fields  map[string]any
	}
	var msgs []msg
	for _, entry := range obj.Entries {
		for _, m := range entry.Msgs {
			if m.Deleted || m.Id == nil {
				continue
			}
			fields := make(map[string]any, len(m.Fields))
			for k, v := range m.Fields {
				fields[k] = v
			}
			msgs = append(msgs, msg{ms: m.Id.Ms, seq: m.Id.Sequence, fields: fields})
		}
	}
	sort.SliceStable(msgs, func(i, j int) bool {
		if msgs[i].ms != msgs[j].ms {
			return msgs[i].ms < msgs[j].ms
		}
		return msgs[i].seq < msgs[j].seq
	})
	out := make([]any, len(msgs))
	for i, m := range msgs {
		out[i] = map[string]any{
			"id":     fmt.Sprintf("%d-%d", m.ms, m.seq),
			"fields": m.fields,
		}
	}
	return out
}

// bytesToStrings widens a [][]byte to []string.
func bytesToStrings(bs [][]byte) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = string(b)
	}
	return out
}

// bytesToAny widens a [][]byte to []any of strings so it marshals as a JSON array.
func bytesToAny(bs [][]byte) []any {
	out := make([]any, len(bs))
	for i, b := range bs {
		out[i] = string(b)
	}
	return out
}

// stringsToAny widens a []string to []any.
func stringsToAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

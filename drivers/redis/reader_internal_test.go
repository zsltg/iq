package redis

import (
	"context"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

// TestReaderPresence pins the presence half of every reader's contract, which a
// live keyspace cannot exercise: Redis deletes a hash, list, set or sorted set
// the moment it empties, so an empty reply only ever arrives when the key went
// away between the TYPE and value pipelines. Driving the readers over
// preset replies stages exactly that race.
//
// A stream is the exception — it survives losing every entry — and a RedisJSON
// document may legitimately be the JSON null, which is why presence rides beside
// the value rather than being read off it.
func TestReaderPresence(t *testing.T) {
	ctx := context.Background()
	hash := func(v map[string]string) reader {
		c := goredis.NewMapStringStringCmd(ctx)
		c.SetVal(v)
		return hashReader{c}
	}
	strSlice := func(v []string) *goredis.StringSliceCmd {
		c := goredis.NewStringSliceCmd(ctx)
		c.SetVal(v)
		return c
	}
	zset := func(v []goredis.Z) reader {
		c := goredis.NewZSliceCmd(ctx)
		c.SetVal(v)
		return zsetReader{c}
	}
	stream := func(v []goredis.XMessage) reader {
		c := goredis.NewXMessageSliceCmd(ctx)
		c.SetVal(v)
		return streamReader{c}
	}
	str := func(v string) reader {
		c := goredis.NewStringCmd(ctx)
		c.SetVal(v)
		return stringReader{c}
	}
	// JSONCmd has no exported constructor, but its zero value plus SetVal/SetErr
	// is exactly what the pipeline hands the reader once it has executed.
	json := func(v string) reader {
		c := &goredis.JSONCmd{}
		c.SetVal(v)
		return jsonReader{cmd: c, decimal: numfmt.DecimalAuto}
	}

	tests := []struct {
		name        string
		reader      reader
		wantPresent bool
		wantValue   any
	}{
		{"missing key", missingReader{}, false, nil},

		{"empty hash means the key vanished", hash(map[string]string{}), false, nil},
		{"one-field hash is present", hash(map[string]string{"a": "1"}), true, map[string]any{"a": "1"}},

		{"empty list means the key vanished", listReader{strSlice(nil)}, false, nil},
		{"one-element list is present", listReader{strSlice([]string{"a"})}, true, []any{"a"}},

		{"empty set means the key vanished", setReader{strSlice(nil)}, false, nil},
		{"one-member set is present", setReader{strSlice([]string{"a"})}, true, []any{"a"}},

		{"empty sorted set means the key vanished", zset(nil), false, nil},
		{"one-member sorted set is present", zset([]goredis.Z{{Member: "a", Score: 1}}), true, []any{map[string]any{"member": "a", "score": float64(1)}}},

		// A stream can legitimately hold no entries and still exist.
		{"empty stream is still present", stream(nil), true, []any{}},

		{"empty string value is present", str(""), true, ""},

		// The case that forces presence to be its own channel.
		{"stored JSON null is present", json("null"), true, nil},
		{"stored JSON object is present", json(`{"a":1}`), true, map[string]any{"a": 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, present, err := tt.reader.normalize()

			require.NoError(t, err)
			require.Equal(t, tt.wantPresent, present)
			require.Equal(t, tt.wantValue, v)
		})
	}
}

// TestReaderRaceReadsAsAbsent covers the other way a key can vanish mid-read:
// the value command itself comes back with redis.Nil because the key was deleted
// after TYPE reported it. That is absence, not a null value.
func TestReaderRaceReadsAsAbsent(t *testing.T) {
	ctx := context.Background()

	sc := goredis.NewStringCmd(ctx)
	sc.SetErr(goredis.Nil)
	jc := &goredis.JSONCmd{}
	jc.SetErr(goredis.Nil)

	tests := []struct {
		name   string
		reader reader
	}{
		{"string read raced", stringReader{sc}},
		{"json read raced", jsonReader{cmd: jc, decimal: numfmt.DecimalAuto}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, present, err := tt.reader.normalize()

			require.NoError(t, err, "a vanished key is not an error")
			require.False(t, present)
			require.Nil(t, v)
		})
	}
}

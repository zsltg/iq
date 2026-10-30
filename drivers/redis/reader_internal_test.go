package redis

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/rawpred"
)

// TestReaderPresence pins the presence half of the contract of every reader,
// which a live keyspace cannot exercise. Redis deletes a hash, list, set or
// sorted set the moment it empties. So an empty reply only arrives when the key
// went away between the TYPE and value pipelines. Driving the readers over
// preset replies stages exactly that race.
//
// A stream is the exception, because it survives losing every entry. A RedisJSON
// document can be the JSON null. This is why presence rides beside the value and
// is not read off it.
func TestReaderPresence(t *testing.T) {
	ctx := context.Background()
	hash := func(flat ...string) reader {
		return hashReader{onePage(ctx), scanReply(ctx, flat...)}
	}
	strSlice := func(v []string) *goredis.StringSliceCmd {
		c := goredis.NewStringSliceCmd(ctx)
		c.SetVal(v)
		return c
	}
	zset := func(flat ...string) reader {
		return zsetReader{onePage(ctx), scanReply(ctx, flat...)}
	}
	stream := func(v []goredis.XMessage) reader {
		c := goredis.NewXMessageSliceCmd(ctx)
		c.SetVal(v)
		return streamReader{onePage(ctx), c}
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

		{"empty hash means the key vanished", hash(), false, nil},
		{"one-field hash is present", hash("a", "1"), true, map[string]any{"a": "1"}},

		{"empty list means the key vanished", listReader{onePage(ctx), strSlice(nil)}, false, nil},
		{"one-element list is present", listReader{onePage(ctx), strSlice([]string{"a"})}, true, []any{"a"}},

		{"empty set means the key vanished", setReader{onePage(ctx), scanReply(ctx)}, false, nil},
		{"one-member set is present", setReader{onePage(ctx), scanReply(ctx, "a")}, true, []any{"a"}},

		{"empty sorted set means the key vanished", zset(), false, nil},
		{"one-member sorted set is present", zset("a", "1"), true, []any{map[string]any{"member": "a", "score": float64(1)}}},

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

// TestReaderRaceReadsAsAbsent covers the other way that a key can vanish
// mid-read. The value command itself comes back with redis.Nil because the key
// was deleted after TYPE reported it. That is absence, not a null value.
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

// TestReaderReportsReadError proves each reader returns the error of its reply,
// so a failed read is not taken for an absent key.
func TestReaderReportsReadError(t *testing.T) {
	ctx := context.Background()
	errRead := errors.New("read failed")

	hc := goredis.NewScanCmd(ctx, nil)
	hc.SetErr(errRead)
	lc := goredis.NewStringSliceCmd(ctx)
	lc.SetErr(errRead)
	sc := goredis.NewScanCmd(ctx, nil)
	sc.SetErr(errRead)
	zc := goredis.NewScanCmd(ctx, nil)
	zc.SetErr(errRead)
	xc := goredis.NewXMessageSliceCmd(ctx)
	xc.SetErr(errRead)
	jc := &goredis.JSONCmd{}
	jc.SetErr(errRead)
	fc := &goredis.JSONCmd{}
	fc.SetErr(errRead)

	tests := []struct {
		name   string
		reader reader
	}{
		{"hash", hashReader{onePage(ctx), hc}},
		{"list", listReader{onePage(ctx), lc}},
		{"set", setReader{onePage(ctx), sc}},
		{"sorted set", zsetReader{onePage(ctx), zc}},
		{"stream", streamReader{onePage(ctx), xc}},
		{"json", jsonReader{cmd: jc, decimal: numfmt.DecimalAuto}},
		{"filtered json", filterJSONReader{cmd: fc, decimal: numfmt.DecimalAuto, matcher: rawpred.NewMatcher(nil, numfmt.DecimalAuto)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, present, err := tt.reader.normalize()

			require.ErrorIs(t, err, errRead)
			require.False(t, present)
			require.Nil(t, v)
		})
	}
}

// TestReaderReportsMalformedJSON proves both RedisJSON readers return the decode
// error of a reply that is not valid JSON, rather than a present null.
func TestReaderReportsMalformedJSON(t *testing.T) {
	jc := &goredis.JSONCmd{}
	jc.SetVal("{")
	fc := &goredis.JSONCmd{}
	fc.SetVal("{")

	tests := []struct {
		name   string
		reader reader
	}{
		{"json", jsonReader{cmd: jc, decimal: numfmt.DecimalAuto}},
		{"filtered json", filterJSONReader{cmd: fc, decimal: numfmt.DecimalAuto, matcher: rawpred.NewMatcher(nil, numfmt.DecimalAuto)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, present, err := tt.reader.normalize()

			require.ErrorContains(t, err, "decode redis json")
			require.False(t, present)
			require.Nil(t, v)
		})
	}
}

// TestZsetReaderNonFinite makes sure that an infinite or NaN score never reaches
// the value as a float64. JSON has no form for it, so the value carries the text
// that Redis uses.
func TestZsetReaderNonFinite(t *testing.T) {
	tests := []struct {
		name  string
		score float64
		want  any
	}{
		{"normal", 2.5, 2.5},
		{"positive infinity", math.Inf(1), "inf"},
		{"negative infinity", math.Inf(-1), "-inf"},
		{"nan", math.NaN(), "nan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			score := strconv.FormatFloat(tt.score, 'g', -1, 64)
			got, ok, err := zsetReader{onePage(ctx), scanReply(ctx, "m", score)}.normalize()
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, []any{map[string]any{"member": "m", "score": tt.want}}, got)
			_, err = json.Marshal(got)
			require.NoError(t, err)
		})
	}
}

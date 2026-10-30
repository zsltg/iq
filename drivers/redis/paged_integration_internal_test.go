package redis

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// bigKeyElements is the element count of each key in TestPagedReadOfLargeKeys.
// It is more than two chunks of readChunk, so a read needs three rounds.
const bigKeyElements = 2500

// TestPagedReadOfLargeKeys runs the paged read against a real server. For each
// type that has elements, it writes one key with more than two chunks. It reads
// the key with Get and with ScanBatches. It compares the result with the value
// that one whole-key command gives, normalized in the same way. It also checks
// the element limit, and the unbounded opt-in, on the same key.
func TestPagedReadOfLargeKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping redis integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := openOnDB(t, ctx, numfmt.DecimalAuto)

	tests := []struct {
		name  string
		seed  func(p goredis.Pipeliner, key string)
		whole func(key string) (any, error)
	}{
		{
			name: "hash",
			seed: func(p goredis.Pipeliner, key string) {
				for i := range bigKeyElements {
					p.HSet(ctx, key, fmt.Sprintf("f%04d", i), fmt.Sprintf("v%d", i))
				}
			},
			whole: func(key string) (any, error) {
				m, err := store.client.HGetAll(ctx, key).Result()
				out := map[string]any{}
				for f, v := range m {
					out[f] = v
				}
				return out, err
			},
		},
		{
			name: "list",
			seed: func(p goredis.Pipeliner, key string) {
				for i := range bigKeyElements {
					// Every tenth element repeats, so the list is not a set.
					p.RPush(ctx, key, fmt.Sprintf("e%d", i-i%10))
				}
			},
			whole: func(key string) (any, error) {
				l, err := store.client.LRange(ctx, key, 0, -1).Result()
				return toAny(l), err
			},
		},
		{
			name: "set",
			seed: func(p goredis.Pipeliner, key string) {
				for i := range bigKeyElements {
					p.SAdd(ctx, key, fmt.Sprintf("m%04d", i))
				}
			},
			whole: func(key string) (any, error) {
				l, err := store.client.SMembers(ctx, key).Result()
				sort.Strings(l)
				return toAny(l), err
			},
		},
		{
			name: "zset",
			seed: func(p goredis.Pipeliner, key string) {
				for i := range bigKeyElements {
					// Ten members share each score, so the member breaks the tie.
					p.ZAdd(ctx, key, goredis.Z{Score: float64(i / 10), Member: fmt.Sprintf("m%04d", (i*7)%bigKeyElements)})
				}
			},
			whole: func(key string) (any, error) {
				zs, err := store.client.ZRangeWithScores(ctx, key, 0, -1).Result()
				out := make([]any, 0, len(zs))
				for _, z := range zs {
					out = append(out, map[string]any{"member": z.Member, "score": z.Score})
				}
				return out, err
			},
		},
		{
			name: "stream",
			seed: func(p goredis.Pipeliner, key string) {
				for i := range bigKeyElements {
					p.XAdd(ctx, &goredis.XAddArgs{Stream: key, ID: fmt.Sprintf("%d-0", i+1), Values: map[string]any{"f": fmt.Sprintf("v%d", i)}})
				}
			},
			whole: func(key string) (any, error) {
				ms, err := store.client.XRange(ctx, key, "-", "+").Result()
				out := make([]any, 0, len(ms))
				for _, m := range ms {
					fields := map[string]any{}
					maps.Copy(fields, m.Values)
					out = append(out, map[string]any{"id": m.ID, "fields": fields})
				}
				return out, err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "iq:test:paged:" + tt.name
			t.Cleanup(func() {
				cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer ccancel()
				if err := store.client.Del(cctx, key).Err(); err != nil {
					t.Errorf("delete %s: %v", key, err)
				}
			})
			p := store.client.Pipeline()
			tt.seed(p, key)
			_, err := p.Exec(ctx)
			require.NoError(t, err)
			want, err := tt.whole(key)
			require.NoError(t, err)
			require.Len(t, want, bigKeyElements, "the whole-key command sees every element")

			got, err := store.Get(ctx, []string{key})
			require.NoError(t, err)
			require.Equal(t, map[string]any{key: want}, got, "Get")

			var scanned any
			err = store.ScanBatches(ctx, func(batch map[string]any) error {
				if v, ok := batch[key]; ok {
					scanned = v
				}
				return nil
			})
			require.NoError(t, err)
			require.Equal(t, want, scanned, "ScanBatches")

			// Lower the limit below the size of the key. The real size comes from
			// the server, so this proves the refusal on real replies.
			store.limits = limits{maxElements: 1000, chunk: readChunk}
			t.Cleanup(func() { store.limits = defaultLimits() })
			_, err = store.Get(ctx, []string{key})
			require.ErrorIs(t, err, ErrKeyTooLarge)

			got, err = store.Get(query.WithUnbounded(ctx), []string{key})
			require.NoError(t, err)
			require.Equal(t, map[string]any{key: want}, got, "unbounded Get")
		})
	}
}

// toAny converts a string slice to the []any that the normalizers give.
func toAny(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

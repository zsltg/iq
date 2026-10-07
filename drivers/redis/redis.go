// Package redis adapts a Redis server to the query.Store port using go-redis.
package redis

import (
	"context"
	"errors"
	"fmt"
	"io"

	goredis "github.com/redis/go-redis/v9"

	"github.com/zsltg/iq/internal/numfmt"
)

func init() {
	// Silence go-redis's global logger so its internal pool messages never reach
	// the user; the CLI surfaces its own clean errors instead.
	goredis.SetLogger(noopLogger{})
}

// noopLogger discards go-redis's internal log output.
type noopLogger struct{}

func (noopLogger) Printf(_ context.Context, _ string, _ ...any) {}

// Store is a query.Store backed by Redis.
type Store struct {
	client *goredis.Client
	// pageSize bounds how many keys ScanBatches accumulates before fetching and
	// handing off a page, so streaming memory stays O(pageSize). Defaults to
	// scanCount; tests lower it to exercise multi-page behavior.
	pageSize int
	// decimal chooses how a RedisJSON fractional number is presented to the
	// filter: a float64 (auto/number) or its exact literal string.
	decimal numfmt.DecimalMode
}

// ErrInvalidURI reports a connection URI that go-redis cannot parse.
var ErrInvalidURI = errors.New("parse redis url: the connection URI is not valid (percent-encode special characters in the password)")

// parseURL parses a redis:// connection URI. The go-redis parse error can quote
// a part of the password, for example after an unescaped slash. So this function
// drops that error text and returns ErrInvalidURI.
func parseURL(uri string) (*goredis.Options, error) {
	opts, err := goredis.ParseURL(uri)
	if err != nil {
		return nil, ErrInvalidURI
	}
	return opts, nil
}

// Open connects to the Redis server named by a redis:// URL. It verifies the
// connection with a PING so a bad URL or unreachable server fails fast at
// startup rather than on the first query. When trace is non-nil, every subsequent
// command is logged to it (the CLI's --verbose trace); the startup PING is not
// traced, as the hook is attached only after it succeeds. dec chooses how
// RedisJSON fractional numbers are presented to the filter.
func Open(ctx context.Context, url string, trace io.Writer, dec numfmt.DecimalMode) (*Store, error) {
	opts, err := parseURL(url)
	if err != nil {
		return nil, err
	}
	if opts.Protocol == 0 {
		// Default to RESP2 so aggregate replies arrive as flat arrays and match
		// redis-cli's classic output rather than RESP3 maps and doubles.
		opts.Protocol = 2
	}
	client := goredis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect redis: %w", err)
	}
	if trace != nil {
		client.AddHook(newTraceHook(trace))
	}
	return &Store{client: client, pageSize: scanCount, decimal: dec}, nil
}

// Query forwards args to Redis as a single command and returns the raw result.
// A nil reply (for example a missing key) is returned as a nil result, not an
// error.
func (s *Store) Query(ctx context.Context, args []string) (any, error) {
	cmd := make([]any, len(args))
	for i, a := range args {
		cmd[i] = a
	}
	result, err := s.client.Do(ctx, cmd...).Result()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	return result, nil
}

// Close releases the underlying client's connection pool.
func (s *Store) Close() error {
	return s.client.Close()
}

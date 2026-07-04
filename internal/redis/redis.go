// Package redis adapts a Redis server to the query.Store port using go-redis.
package redis

import (
	"context"
	"errors"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
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
}

// Open connects to the Redis server named by a redis:// URL. It verifies the
// connection with a PING so a bad URL or unreachable server fails fast at
// startup rather than on the first query.
func Open(ctx context.Context, url string) (*Store, error) {
	opts, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	client := goredis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect redis: %w", err)
	}
	return &Store{client: client}, nil
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

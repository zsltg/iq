// Package redis adapts a Redis server to the query.Store port using go-redis.
package redis

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/zsltg/iq/internal/numfmt"
)

func init() {
	// Silence the global logger of go-redis so its internal pool messages never
	// reach the user. The CLI surfaces its own clean errors instead.
	goredis.SetLogger(noopLogger{})
}

// noopLogger discards go-redis's internal log output.
type noopLogger struct{}

func (noopLogger) Printf(_ context.Context, _ string, _ ...any) {}

// Store is a query.Store backed by Redis.
type Store struct {
	client *goredis.Client
	// pageSize bounds how many keys ScanBatches accumulates before fetching and
	// handing off a page, so streaming memory stays O(pageSize). It defaults
	// to scanCount. Tests lower it to exercise multi-page behavior.
	pageSize int
	// decimal chooses how a RedisJSON fractional number is presented to the
	// filter: a float64 (auto/number) or its exact literal string.
	decimal numfmt.DecimalMode
	// limits holds the size limits of one key read. It defaults to the package
	// constants. Tests lower it to cover paging with small keys.
	limits limits
}

// ErrInvalidURI reports a connection URI that go-redis cannot parse.
var ErrInvalidURI = errors.New("parse redis url: the connection URI is not valid")

// parseURL parses a redis:// connection URI. The go-redis parse error can quote
// a part of the password, for example after an unescaped slash. So this function
// drops that error text and returns ErrInvalidURI. It adds a hint about
// percent-encoding only when the URI seems to hold a password that was not read
// as userinfo.
func parseURL(uri string) (*goredis.Options, error) {
	opts, err := goredis.ParseURL(uri)
	if err != nil {
		if looksLikeUnreadPassword(uri) {
			return nil, fmt.Errorf("%w (percent-encode special characters in the password)", ErrInvalidURI)
		}
		return nil, ErrInvalidURI
	}
	return opts, nil
}

// looksLikeUnreadPassword reports whether the URI holds an "@" that net/url did
// not read as userinfo. A password with an unescaped special character causes this.
func looksLikeUnreadPassword(uri string) bool {
	if !strings.Contains(uri, "@") {
		return false
	}
	u, err := url.Parse(uri)
	return err != nil || u.User == nil
}

// Open connects to the Redis server named by a redis:// URL. A PING fails fast on a
// bad URL or an unreachable server, at startup and not on the first query. If trace
// is non-nil, the driver logs every later command to it (the CLI's --verbose trace).
// The startup PING is not traced, because the hook attaches after it succeeds. dec
// sets how the filter sees RedisJSON fractional numbers.
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
	return &Store{client: client, pageSize: scanCount, decimal: dec, limits: defaultLimits()}, nil
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

package redis_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// TestMain provisions the test Redis before the suite runs. flag.Parse must run
// before testing.Short is read, and os.Exit skips deferred cleanup, so the work
// lives in runTests where the defer fires before the process exits.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	flag.Parse()
	if !testing.Short() {
		ctx := context.Background()
		// Use the named external server, else stand up an ephemeral container.
		url := os.Getenv("IQ_REDIS_URL")
		if url == "" {
			container, err := tcredis.Run(ctx, "redis:8")
			if err != nil {
				fmt.Fprintf(os.Stderr, "start redis container: %v\n", err)
				return 1
			}
			defer func() { _ = testcontainers.TerminateContainer(container) }()

			url, err = container.ConnectionString(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "redis connection string: %v\n", err)
				return 1
			}
		}
		// Pin the tests to a reserved database, then export it through
		// IQ_REDIS_URL so both this (redis_test) and the internal redis test
		// package in this binary pick it up: an internal package test cannot see
		// this file's variables. The reserved DB keeps integration runs off DB 0,
		// which developers seed for manual exploration, even when IQ_REDIS_URL
		// names a shared server (as the mutation gate does).
		pinned, err := withRedisDB(url, testRedisDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pin redis test database: %v\n", err)
			return 1
		}
		_ = os.Setenv("IQ_REDIS_URL", pinned)
	}
	return m.Run()
}

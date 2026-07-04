# iq

A Go command-line tool that forwards queries to NoSQL databases. Redis is the first supported
backend; the query core is driver-agnostic so further backends slot in behind the same port.

## Requirements

- Go 1.25+
- Docker (optional, for the local Redis used by integration tests)

## Build

```bash
go build -o iq .
```

## Usage

`iq query` forwards a command to the database and prints the result:

```bash
./iq query SET greeting hello   # "OK"
./iq query GET greeting         # "hello"
./iq query INCR counter         # (integer) 1
./iq query GET missing          # (nil)
```

Output mirrors redis-cli's cooked style: bulk strings quoted, integers as `(integer) N`, a
missing value as `(nil)`, and arrays (lists, sets, `HGETALL`, sorted sets with `WITHSCORES`) as a
numbered, indented list. The client uses RESP2 so aggregate replies match redis-cli's classic flat
output. Status replies such as `OK` and `PONG` appear quoted, a limitation of the underlying client,
which does not distinguish them from bulk strings.

### Connection

The database is addressed with a standard Redis connection URL
(`redis://[user:pass@]host:port[/db]`, `rediss://` for TLS), resolved in this order:

1. the `--url` / `-u` flag
2. the `IQ_REDIS_URL` environment variable
3. the default `redis://localhost:6379/0`

`--timeout` (default `5s`) bounds each query.

## Common commands

```bash
go build -o iq .          # build the binary
go test -short ./...      # fast unit tests, no external services
docker compose up -d --wait   # start a local Redis (redis:latest) on :6379
go test ./...             # full suite, including Redis integration tests
docker compose down       # stop the local Redis
gofumpt -w . && goimports -w .   # format
go vet ./... && golangci-lint run   # vet and lint
govulncheck ./...         # dependency vulnerability scan
bash scripts/mutation-gate.sh   # mutation gate (run with Redis up; fails on any survivor/timeout)
```

Integration tests skip under `go test -short`; the full `go test ./...` needs Redis up (via
`docker compose up`) and connects to `IQ_REDIS_URL` or the local default.

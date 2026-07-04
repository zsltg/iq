#!/usr/bin/env bash
# Seed the local Redis (from compose.yaml) with a small example dataset spanning
# Redis data types, so `iq` has something to explore. The data is ephemeral:
# `docker compose down` discards it. Re-run any time to reset.
set -euo pipefail

if ! docker compose exec -T redis redis-cli ping >/dev/null 2>&1; then
  echo "Redis is not reachable. Start it first: docker compose up -d --wait" >&2
  exit 1
fi

docker compose exec -T redis redis-cli >/dev/null <<'EOF'
FLUSHALL
SET catalog:name "iq Bookshop"
SET books:count 4
HSET book:1 title "The Go Programming Language" author "Donovan and Kernighan" year 2015 price 39
HSET book:2 title "Designing Data-Intensive Applications" author "Martin Kleppmann" year 2017 price 45
HSET book:3 title "A Philosophy of Software Design" author "John Ousterhout" year 2018 price 20
HSET book:4 title "Clean Code" author "Robert C. Martin" year 2008 price 35
SET stock:1 12
SET stock:2 7
SET stock:3 0
SET stock:4 25
SADD genre:programming 1 3 4
SADD genre:architecture 2 3
RPUSH cart:alice 2 3 3 1
ZADD bestsellers 320 1 540 2 210 3 480 4
RPUSH searches "go concurrency" "distributed systems" "refactoring"
SET session:alice tok_abc123 EX 3600
XADD orders 1-1 book 2 qty 1 customer alice
XADD orders 2-1 book 4 qty 2 customer bob
XADD orders 3-1 book 1 qty 1 customer alice
JSON.SET store:profile $ '{"address":{"city":"Budapest","street":"Book St 1"},"hours":{"weekday":"9-18","weekend":"10-14"},"channels":["web","store"]}'
EOF

count=$(docker compose exec -T redis redis-cli DBSIZE | tr -dc '0-9')
echo "Seeded example data: ${count} keys. Try: iq '.[\"book:2\"]', iq '.orders', iq '.[\"store:profile\"].hours'"

#!/usr/bin/env bash
# Seed the local Cassandra (from compose.yaml) with a small example dataset, so
# `iq` has a table to explore. The data is ephemeral: `docker compose down`
# discards it. Re-run any time to reset. The books table uses an int primary key,
# so the jq key mapping (key = the bare primary-key value) is obvious.
set -euo pipefail

if ! docker compose exec -T cassandra cqlsh -e 'describe keyspaces' >/dev/null 2>&1; then
  echo "Cassandra is not reachable. Start it first: docker compose up -d --wait cassandra" >&2
  echo "(Cassandra takes ~1 minute to become ready.)" >&2
  exit 1
fi

docker compose exec -T cassandra cqlsh >/dev/null <<'EOF'
CREATE KEYSPACE IF NOT EXISTS iq WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1};
DROP TABLE IF EXISTS iq.books;
CREATE TABLE iq.books (id int PRIMARY KEY, title text, author text, year int, price int, tags set<text>);
INSERT INTO iq.books (id, title, author, year, price, tags) VALUES (1, 'The Go Programming Language', 'Donovan and Kernighan', 2015, 39, {'go', 'programming'});
INSERT INTO iq.books (id, title, author, year, price, tags) VALUES (2, 'Designing Data-Intensive Applications', 'Martin Kleppmann', 2017, 45, {'data', 'architecture'});
INSERT INTO iq.books (id, title, author, year, price, tags) VALUES (3, 'A Philosophy of Software Design', 'John Ousterhout', 2018, 20, {'design'});
INSERT INTO iq.books (id, title, author, year, price, tags) VALUES (4, 'Clean Code', 'Robert C. Martin', 2008, 35, {'craft', 'design'});
EOF

count=$(docker compose exec -T cassandra cqlsh -e 'SELECT COUNT(*) FROM iq.books' | tr -dc '0-9' | head -c 1)
echo "Seeded example data: ${count} rows in iq.books. Try: iq add cassandra://localhost:9042/iq?table=books -n books && iq --src books '.[\"2\"]'"

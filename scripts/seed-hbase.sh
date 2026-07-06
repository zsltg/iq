#!/usr/bin/env bash
# Seed the local HBase (from compose.yaml) with a small example dataset, so `iq` has
# a table to explore. The data is ephemeral: `docker compose down` discards it. Re-run
# any time to reset. The books table keys rows by a string row key ("1".."4") and
# stores every cell as text under the "cf" column family, so the honest default cell
# encoding (UTF-8 text) renders them readably.
set -euo pipefail

if ! echo 'status' | docker compose exec -T hbase hbase shell -n >/dev/null 2>&1; then
  echo "HBase is not reachable. Start it first: docker compose up -d --wait hbase" >&2
  echo "(HBase takes ~1-2 minutes to become ready.)" >&2
  exit 1
fi

docker compose exec -T hbase hbase shell -n >/dev/null <<'EOF'
disable 'iq_books' rescue nil
drop 'iq_books' rescue nil
create 'iq_books', 'cf'
put 'iq_books', '1', 'cf:title', 'The Go Programming Language'
put 'iq_books', '1', 'cf:author', 'Donovan and Kernighan'
put 'iq_books', '1', 'cf:year', '2015'
put 'iq_books', '2', 'cf:title', 'Designing Data-Intensive Applications'
put 'iq_books', '2', 'cf:author', 'Martin Kleppmann'
put 'iq_books', '2', 'cf:year', '2017'
put 'iq_books', '3', 'cf:title', 'A Philosophy of Software Design'
put 'iq_books', '3', 'cf:author', 'John Ousterhout'
put 'iq_books', '3', 'cf:year', '2018'
put 'iq_books', '4', 'cf:title', 'Clean Code'
put 'iq_books', '4', 'cf:author', 'Robert C. Martin'
put 'iq_books', '4', 'cf:year', '2008'
EOF

count=$(echo "count 'iq_books'" | docker compose exec -T hbase hbase shell -n 2>/dev/null | grep -oE '[0-9]+ row\(s\)' | grep -oE '^[0-9]+')
echo "Seeded example data: ${count:-?} rows in iq_books. Try: iq add hbase://localhost:2181/?table=iq_books -n books && iq --src books '.[\"2\"]'"

#!/usr/bin/env bash
# Seed the local MongoDB (from compose.yaml) with a small example dataset, so
# `iq` has a document collection to explore. The data is ephemeral: `docker
# compose down` discards it. Re-run any time to reset. The books collection uses
# string _ids ("1".."4") so the jq key mapping (key = _id) is obvious.
set -euo pipefail

if ! docker compose exec -T mongo mongosh --quiet --eval 'db.adminCommand("ping").ok' >/dev/null 2>&1; then
  echo "MongoDB is not reachable. Start it first: docker compose up -d --wait" >&2
  exit 1
fi

docker compose exec -T mongo mongosh --quiet iq >/dev/null <<'EOF'
db.books.drop()
db.books.insertMany([
  {_id: "1", title: "The Go Programming Language", author: "Donovan and Kernighan", year: 2015, price: 39, tags: ["go", "programming"]},
  {_id: "2", title: "Designing Data-Intensive Applications", author: "Martin Kleppmann", year: 2017, price: 45, tags: ["data", "architecture"]},
  {_id: "3", title: "A Philosophy of Software Design", author: "John Ousterhout", year: 2018, price: 20, tags: ["design"]},
  {_id: "4", title: "Clean Code", author: "Robert C. Martin", year: 2008, price: 35, tags: ["craft", "design"]}
])
EOF

count=$(docker compose exec -T mongo mongosh --quiet iq --eval 'db.books.countDocuments({})' | tr -dc '0-9')
echo "Seeded example data: ${count} documents in iq.books. Try: iq -u mongodb://localhost:27017/iq -c books '.[\"2\"]'"

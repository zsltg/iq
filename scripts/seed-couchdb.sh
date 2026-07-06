#!/usr/bin/env bash
# Seed the local CouchDB (from compose.yaml) with a small example dataset, so `iq`
# has a document database to explore. The data is ephemeral: `docker compose down`
# discards it. Re-run any time to reset. The iq database uses string _ids ("1".."4")
# so the jq key mapping (key = _id) is obvious.
#
# Needs curl. CouchDB uses HTTP basic auth; the admin credentials match the
# compose service (COUCHDB_USER/COUCHDB_PASSWORD).
set -euo pipefail

base=http://admin:password@localhost:5984
db=iq

if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required to seed CouchDB." >&2
  exit 1
fi

if ! curl -fsS "http://localhost:5984/_up" >/dev/null 2>&1; then
  echo "CouchDB is not reachable at localhost:5984. Start it first: docker compose up -d --wait couchdb" >&2
  exit 1
fi

# Recreate the database so a re-run resets it.
curl -fsS -X DELETE "$base/$db" >/dev/null 2>&1 || true
curl -fsS -X PUT "$base/$db" >/dev/null

curl -fsS -X POST "$base/$db/_bulk_docs" -H 'Content-Type: application/json' -d '{
  "docs": [
    {"_id": "1", "title": "The Go Programming Language", "author": "Donovan and Kernighan", "year": 2015, "price": 39, "tags": ["go", "programming"]},
    {"_id": "2", "title": "Designing Data-Intensive Applications", "author": "Martin Kleppmann", "year": 2017, "price": 45, "tags": ["data", "architecture"]},
    {"_id": "3", "title": "A Philosophy of Software Design", "author": "John Ousterhout", "year": 2018, "price": 20, "tags": ["design"]},
    {"_id": "4", "title": "Clean Code", "author": "Robert C. Martin", "year": 2008, "price": 35, "tags": ["craft", "design"]}
  ]
}' >/dev/null

count=$(curl -fsS "$base/$db" | grep -o '"doc_count":[0-9]*' | cut -d: -f2)
echo "Seeded example data: ${count} documents in ${db}. Try: iq add 'couchdb://admin:password@localhost:5984/?database=${db}' -n books && iq --src books '.[\"2\"]'"

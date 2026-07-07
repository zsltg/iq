#!/usr/bin/env bash
# Seed the local OpenSearch (from compose.yaml) with a small example dataset, so `iq`
# has an index to explore. The data is ephemeral: `docker compose down` discards it.
# Re-run any time to reset. The books index uses string _ids ("1".."4") so the jq key
# mapping (key = _id) is obvious.
#
# Needs curl. The compose service runs with the security plugin disabled, so no
# credentials are needed. It is published on 9201 (Elasticsearch holds 9200).
set -euo pipefail

base=http://localhost:9201
index=books

if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required to seed OpenSearch." >&2
  exit 1
fi

if ! curl -fsS "$base/_cluster/health" >/dev/null 2>&1; then
  echo "OpenSearch is not reachable at localhost:9201. Start it first: docker compose up -d --wait opensearch" >&2
  exit 1
fi

# Recreate the index so a re-run resets it.
curl -fsS -X DELETE "$base/$index" >/dev/null 2>&1 || true

# Bulk-index the documents, refreshing so they are immediately searchable. Each pair
# of lines is an action (with the _id) and the document body.
curl -fsS -X POST "$base/$index/_bulk?refresh=true" -H 'Content-Type: application/x-ndjson' --data-binary '
{"index":{"_id":"1"}}
{"title": "The Go Programming Language", "author": "Donovan and Kernighan", "year": 2015, "price": 39, "tags": ["go", "programming"]}
{"index":{"_id":"2"}}
{"title": "Designing Data-Intensive Applications", "author": "Martin Kleppmann", "year": 2017, "price": 45, "tags": ["data", "architecture"]}
{"index":{"_id":"3"}}
{"title": "A Philosophy of Software Design", "author": "John Ousterhout", "year": 2018, "price": 20, "tags": ["design"]}
{"index":{"_id":"4"}}
{"title": "Clean Code", "author": "Robert C. Martin", "year": 2008, "price": 35, "tags": ["craft", "design"]}
' >/dev/null

count=$(curl -fsS "$base/$index/_count" | grep -o '"count":[0-9]*' | cut -d: -f2)
echo "Seeded example data: ${count} documents in ${index}. Try: iq add 'opensearch://localhost:9201/?index=${index}' -n osbooks && iq --src osbooks '.[\"2\"]'"

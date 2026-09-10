#!/usr/bin/env bash
# Seed the local Couchbase (from compose.yaml) with a small example dataset, so `iq`
# has a document database to explore. The data is ephemeral: `docker compose down`
# discards it. Re-run any time to reset. The iq bucket's _default._default collection
# uses string document IDs ("1".."4"), so the jq key mapping (key = document ID) is
# obvious.
#
# The stock couchbase image starts unprovisioned, so this script does the one-time
# idempotent cluster-init, bucket, and primary index before inserting the sample docs.
# It runs couchbase-cli and cbq inside the iq-couchbase container, so no host client is
# needed. The admin credentials match the compose service (Administrator/password).
set -euo pipefail

container=iq-couchbase
user=Administrator
pass=password
# Overridable for harness use: CI provisions the integration-test bucket
# (IQ_SEED_BUCKET=iq_test IQ_SEED_DATA=0) with the same idempotent steps.
bucket="${IQ_SEED_BUCKET:-iq}"
seed_data="${IQ_SEED_DATA:-1}"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required to seed Couchbase." >&2
  exit 1
fi

if ! docker ps --format '{{.Names}}' | grep -q "^${container}$"; then
  echo "Couchbase container ${container} is not running. Start it first: docker compose up -d --wait couchbase" >&2
  exit 1
fi

# Wait until the management port answers before provisioning. The probe sends the
# admin credentials because /pools answers 401 after cluster-init: an unprovisioned
# node accepts the request with or without them, but a restarted, already
# provisioned container refuses the anonymous probe and the wait never completes.
for _ in $(seq 1 30); do
  if docker exec -i "$container" curl -fsS -u "$user:$pass" http://127.0.0.1:8091/pools >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done
if [ "${ready:-0}" != "1" ]; then
  echo "Couchbase did not become ready at ${container}:8091." >&2
  exit 1
fi

# Idempotent cluster-init: fails harmlessly if the cluster is already initialized.
docker exec -i "$container" couchbase-cli cluster-init \
  -c 127.0.0.1:8091 \
  --cluster-username "$user" --cluster-password "$pass" \
  --services data,index,query \
  --cluster-ramsize 512 --cluster-index-ramsize 256 >/dev/null 2>&1 || true

# Idempotent bucket create: ignore "already exists".
docker exec -i "$container" couchbase-cli bucket-create \
  -c 127.0.0.1:8091 -u "$user" -p "$pass" \
  --bucket "$bucket" --bucket-type couchbase --bucket-ramsize 256 --enable-flush 1 >/dev/null 2>&1 || true

# Wait until the query service can create the primary index (it warms up after init).
for _ in $(seq 1 30); do
  if docker exec -i "$container" cbq -e http://127.0.0.1:8093 -u "$user" -p "$pass" -q=true \
    --script="CREATE PRIMARY INDEX IF NOT EXISTS ON \`${bucket}\`;" >/dev/null 2>&1; then
    indexed=1
    break
  fi
  sleep 2
done
if [ "${indexed:-0}" != "1" ]; then
  echo "Couchbase query service did not become ready to create the primary index." >&2
  exit 1
fi

if [ "$seed_data" != "1" ]; then
  echo "Provisioned cluster, bucket ${bucket}, and primary index (no sample data)."
  exit 0
fi

# Only the sample-data path reads a JSON response, so this check sits after the
# provisioning-only exit. CI provisions with IQ_SEED_DATA=0 and must not need jq.
if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required to seed the Couchbase sample data." >&2
  exit 1
fi

# Send one statement at a time to the query REST endpoint and read the status.
# `cbq --script` does not divide the text on ";": it parses the two statements as one
# and stops with `syntax error ... UPSERT (reserved word)`. It also exits 0 after that
# fatal error, so `set -e` cannot see the failure, and the output went to /dev/null.
# A seed that wrote no document therefore reported success.
seed_query() {
  local label="$1" stmt="$2" out
  out=$(docker exec -i "$container" curl -s -u "$user:$pass" \
    http://127.0.0.1:8093/query/service \
    --data-urlencode "statement=$stmt" 2>/dev/null)
  if [ "$(printf '%s' "$out" | jq -r '.status // "none"')" != "success" ]; then
    echo "Seed ${label} failed: $(printf '%s' "$out" | jq -r '.errors[0].msg // "unknown error"')" >&2
    exit 1
  fi
}

seed_query delete "DELETE FROM \`${bucket}\`"
seed_query upsert "UPSERT INTO \`${bucket}\` (KEY, VALUE) VALUES
  ('1', {'title': 'The Go Programming Language', 'author': 'Donovan and Kernighan', 'year': 2015, 'price': 39, 'tags': ['go', 'programming']}),
  ('2', {'title': 'Designing Data-Intensive Applications', 'author': 'Martin Kleppmann', 'year': 2017, 'price': 45, 'tags': ['data', 'architecture']}),
  ('3', {'title': 'A Philosophy of Software Design', 'author': 'John Ousterhout', 'year': 2018, 'price': 20, 'tags': ['design']}),
  ('4', {'title': 'Clean Code', 'author': 'Robert C. Martin', 'year': 2008, 'price': 35, 'tags': ['craft', 'design']})"

# Count through the query REST endpoint, not cbq: cbq writes one JSON object per
# statement and colours its errors, so a parser that reads the whole stream can take
# the wrong object. The endpoint answers with one JSON document. Ask for
# request_plus, because a count through the primary index can otherwise read a stale
# index and report fewer documents than the upsert wrote. The earlier
# `tr -dc '0-9'` kept every digit of the response, the request id and the timings
# included, so a failed insert still printed a long number that looked correct.
count=$(docker exec -i "$container" curl -s -u "$user:$pass" \
  http://127.0.0.1:8093/query/service \
  --data-urlencode "statement=SELECT RAW COUNT(*) FROM \`${bucket}\`" \
  --data-urlencode "scan_consistency=request_plus" 2>/dev/null | jq -r '.results[0] // "none"')
if [ "$count" != "4" ]; then
  echo "Seed verification failed: expected 4 documents in ${bucket}, found '${count}'." >&2
  exit 1
fi
echo "Seeded example data: ${count} documents in ${bucket}. Try: iq add 'couchbase://${user}:${pass}@localhost/?bucket=${bucket}' -n books && iq --src books '.[\"2\"]'"

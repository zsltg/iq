#!/usr/bin/env bash
# Start the compose services that one test package needs and export the
# IQ_*_URL variables that point the package tests at them. With the variables
# set, the tests use the running services and do not start a testcontainer.
# Usage: bash scripts/ci-backend.sh <package>, for example ./drivers/redis.
# The variables go to $GITHUB_ENV, or to standard output if that is not set.
# A package with no backend starts nothing. The deep-mutate job and the
# mutant-proof workflow in .github/workflows both call this script.
# Couchbase ships unprovisioned, so the idempotent seed script runs with the
# integration bucket and no sample data. HBase needs host networking, because
# its native RPC hands back the advertised hostname of the region server.
set -euo pipefail

pkg="${1:?usage: ci-backend.sh <package>}"
out="${GITHUB_ENV:-/dev/stdout}"

case "$pkg" in
  ./cmd)                   svcs="redis mongo"
    echo "IQ_REDIS_URL=redis://localhost:6379/0" >> "$out"
    echo "IQ_MONGO_URL=mongodb://localhost:27017/iq_test" >> "$out" ;;
  ./drivers/redis)         svcs="redis"
    echo "IQ_REDIS_URL=redis://localhost:6379/0" >> "$out" ;;
  ./drivers/mongo)         svcs="mongo"
    echo "IQ_MONGO_URL=mongodb://localhost:27017/iq_test" >> "$out" ;;
  ./drivers/cassandra)     svcs="cassandra"
    echo "IQ_CASSANDRA_URL=cassandra://localhost:9042/iq_test" >> "$out" ;;
  ./drivers/dynamodb)      svcs="dynamodb"
    echo "IQ_DYNAMODB_URL=dynamodb://us-east-1/?endpoint=http://localhost:8000" >> "$out" ;;
  ./drivers/hbase)         svcs="hbase"
    echo "IQ_HBASE_URL=hbase://localhost:2181/" >> "$out" ;;
  ./drivers/couchdb)       svcs="couchdb"
    echo "IQ_COUCHDB_URL=couchdb://admin:password@localhost:5984/" >> "$out" ;;
  ./drivers/couchbase)     svcs="couchbase"
    echo "IQ_COUCHBASE_URL=couchbase://Administrator:password@localhost/?bucket=iq_test" >> "$out" ;;
  ./drivers/neo4j)         svcs="neo4j"
    echo "IQ_NEO4J_URL=neo4j://neo4j:password@localhost:7687/" >> "$out" ;;
  ./drivers/elasticsearch) svcs="elasticsearch opensearch"
    echo "IQ_ELASTICSEARCH_URL=elasticsearch://localhost:9200/" >> "$out"
    echo "IQ_OPENSEARCH_URL=opensearch://localhost:9201/" >> "$out" ;;
  *)                       svcs="" ;;
esac

if [ -n "$svcs" ]; then
  # shellcheck disable=SC2086 # svcs is a space-separated list of service names.
  docker compose up -d --wait $svcs
fi
if [ "$pkg" = "./drivers/couchbase" ]; then
  IQ_SEED_BUCKET=iq_test IQ_SEED_DATA=0 bash scripts/seed-couchbase.sh
fi

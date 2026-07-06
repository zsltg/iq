#!/usr/bin/env bash
# Seed the local Neo4j (from compose.yaml) with a small example graph, so `iq` has a
# property graph to explore. The data is ephemeral: `docker compose down` discards it.
# Re-run any time to reset. Person and Book nodes carry a string `id` ("1".."4") with a
# uniqueness constraint, so a ?key=id source has an obvious, stable key (and writes,
# which upsert by the key, are allowed). WROTE relationships connect them, for the
# inspect view and the relationship follow-up.
#
# Runs cypher-shell inside the iq-neo4j container, so no host client is needed. The
# credentials match the compose service (NEO4J_AUTH=neo4j/password).
set -euo pipefail

container=iq-neo4j
user=neo4j
pass=password

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required to seed Neo4j." >&2
  exit 1
fi

if ! docker ps --format '{{.Names}}' | grep -q "^${container}$"; then
  echo "Neo4j container ${container} is not running. Start it first: docker compose up -d --wait neo4j" >&2
  exit 1
fi

# Neo4j on a JVM is slow to accept connections; wait until cypher-shell can run.
for _ in $(seq 1 30); do
  if docker exec -i "$container" cypher-shell -u "$user" -p "$pass" 'RETURN 1;' >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done
if [ "${ready:-0}" != "1" ]; then
  echo "Neo4j did not become ready at ${container}:7687." >&2
  exit 1
fi

# A single session so the constraints exist before the MERGEs that rely on them.
docker exec -i "$container" cypher-shell -u "$user" -p "$pass" -d neo4j <<'CYPHER'
MATCH (n) DETACH DELETE n;
CREATE CONSTRAINT person_id IF NOT EXISTS FOR (p:Person) REQUIRE p.id IS UNIQUE;
CREATE CONSTRAINT book_id   IF NOT EXISTS FOR (b:Book)   REQUIRE b.id IS UNIQUE;
MERGE (p1:Person {id: '1'}) SET p1.name = 'Alan Donovan',     p1.age = 50;
MERGE (p2:Person {id: '2'}) SET p2.name = 'Martin Kleppmann', p2.age = 42;
MERGE (p3:Person {id: '3'}) SET p3.name = 'John Ousterhout',  p3.age = 70;
MERGE (b1:Book {id: '1'}) SET b1.title = 'The Go Programming Language',          b1.year = 2015, b1.price = 39.0;
MERGE (b2:Book {id: '2'}) SET b2.title = 'Designing Data-Intensive Applications', b2.year = 2017, b2.price = 45.0;
MERGE (b3:Book {id: '3'}) SET b3.title = 'A Philosophy of Software Design',       b3.year = 2018, b3.price = 20.0;
MATCH (p:Person {id:'1'}), (b:Book {id:'1'}) MERGE (p)-[:WROTE]->(b);
MATCH (p:Person {id:'2'}), (b:Book {id:'2'}) MERGE (p)-[:WROTE]->(b);
MATCH (p:Person {id:'3'}), (b:Book {id:'3'}) MERGE (p)-[:WROTE]->(b);
CYPHER

count=$(docker exec -i "$container" cypher-shell -u "$user" -p "$pass" --format plain 'MATCH (n) RETURN count(n);' | tail -n1)
echo "Seeded example graph: ${count} nodes. Try: iq add 'neo4j://neo4j:password@localhost:7687/?label=Person&key=id' -n graph && iq --src graph '.[\"1\"]'"

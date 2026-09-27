---
icon: material/scale-balance
---

# Comparison

How `iq` relates to other query tools. Its niche is narrow, a single static binary that gives
NoSQL stores one jq-based query interface, the filter running client-side over normalized JSON so
semantics are identical across backends.

## By job

| Job | What people use today | What `iq` changes |
| --- | --- | --- |
| Query a live store from a shell | `redis-cli`, `mongosh`, `cqlsh`, `aws dynamodb`, `curl` against Elasticsearch, each piped into `jq` | one language and one config over all of them, the filter names the keys, paging and normalization are handled |
| Inspect a backup | `redis-rdb-tools`, `bsondump`, `mongoexport` files, DynamoDB export JSON, `cqlsh COPY` CSV, APOC JSON, each read by its own tool or by hand | one reader over six formats, queryable, diffable, restorable, with no server |
| Copy or migrate between stores | ad-hoc scripts, `mongodump`/`mongorestore` and `elasticdump` for one store at a time, Redpanda Connect or Bento for any-to-any (a YAML pipeline plus the Bloblang mapping language), Airbyte for a platform | one command, a typed round-trip, an inline jq transform, cross-driver |
| Compare environments, watch schema drift | export both sides, then `diff`, `jd` or `jq` by hand | `iq diff` over data, stats, or inferred schema, with `diff(1)` exit codes for CI |
| Give an AI agent database access | one MCP server per backend (MongoDB's own, Google's MCP Toolbox for Databases), each speaking its native dialect behind a server process | one binary and one language for all of them, `--explain` as a dry run, a skill any agent that reads the Agent Skills format can install, and an MCP server, read-only by default (see [AI agents](agents.md)) |

## What iq is not

- Not an analytics engine. Pushdown covers equality and existence on every backend, ranges on
  MongoDB, CouchDB and Couchbase, regex on MongoDB and CouchDB, and everything else runs as a
  client-side scan, while an aggregate materializes the keyspace behind `--unbounded`. A heavy
  question belongs in the backend's own language through `iq exec`, or in a query engine.
- Not a replacement for the native shell where the backend's own feature is the point:
  aggregation pipelines, relevance scoring, graph traversals, vector search. `iq exec` forwards
  those verbatim rather than modelling them.

## Tools that unify many databases under one language

Legend: ● primary, ◐ partial, — none. Model is the shape the query language speaks, footprint is
what you run.

| Tool | Query language | Relational | NoSQL | Files | Data model | Footprint |
|---|---|:---:|:---:|:---:|---|---|
| **iq** | **jq** | — | **●** | **◐** | **document** | **single binary** |
| [sq](https://sq.io) | SLQ / SQL | ● | — | ● | tabular | single binary |
| [usql](https://github.com/xo/usql) | native SQL | ● | ◐ | — | tabular | single binary (multiplexer) |
| [OctoSQL](https://github.com/cube2222/octosql) | SQL | ● | ◐ | ● | tabular | single binary |
| [DuckDB](https://duckdb.org) | SQL | ◐ | — | ● | tabular | in-process / CLI |
| SQL over files ([dsq](https://github.com/multiprocessio/dsq), [trdsql](https://github.com/noborus/trdsql)) | SQL | — | — | ● | tabular | single binary |
| [Trino](https://trino.io) / [Presto](https://prestodb.io) | SQL | ● | ● | ● | tabular (◐ JSON) | server / engine |
| [Apache Drill](https://drill.apache.org) | SQL | ● | ● | ● | schema-free (both) | server / engine |
| Data virtualization ([Denodo](https://www.denodo.com), [Dremio](https://www.dremio.com), [MindsDB](https://mindsdb.com)) | SQL | ● | ● | ◐ | virtual relational | server |
| Universal clients ([DBeaver](https://dbeaver.io), [DataGrip](https://www.jetbrains.com/datagrip/), [DBX](https://github.com/t8y2/dbx), [LazySQL](https://github.com/jorgerojas26/lazysql)) | native per-backend | ● | ◐ | ◐ | client-side, per backend | desktop app / TUI |
| [Redpanda Connect](https://github.com/redpanda-data/connect) | Bloblang, a mapping language | ◐ | ● | ◐ | document | single binary (YAML pipeline) |
| [MCP Toolbox for Databases](https://github.com/googleapis/genai-toolbox) | native per-backend, as MCP tools | ● | ● | — | per backend | server |

Placement is by each tool's primary targets, several (Trino, Drill, OctoSQL, DuckDB) partially
cover neighbouring columns via connectors or extensions, and `iq` covers files the same way, a
read-only `file://` source over database dumps, not arbitrary files.

`sq`, the tool `iq`'s command set is modelled on, unifies relational databases and files,
and never covers NoSQL. Language specs and embedded libraries (PartiQL, SQL++ / N1QL, JSONiq,
Apache Calcite, GraphQL federation) span nested and tabular data too, but they are
specifications or components inside an engine, not something anyone runs instead of a CLI.

The takeaway is the NoSQL column paired with footprint, among these tools, `iq` is the only
single binary that gives NoSQL stores one query language. What covers more runs as a server
(Trino, Drill, the virtualization platforms, the MCP Toolbox), and what is as light either
speaks each backend's own dialect (usql, the universal clients) or targets files and relational
stores instead (sq, DuckDB, dsq). Redpanda Connect is a single binary too, but Bloblang maps
records through a pipeline, it is not a query language you type at a shell.

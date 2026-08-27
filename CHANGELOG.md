# CHANGELOG

All notable changes to this project are documented here.
This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
and its commits follow [Conventional Commits](https://www.conventionalcommits.org/).

## v0.34.2 - 2026-08-27
### Documentation
- replace the pre-1.0 warning with an ai-assistance note (dd2b35d)
- **see-also:** map the jq ecosystem (dc6d381)

### Bug Fixes
- **deps:** bump go-archive, otel and x/net past their advisories (4ceda38)

### Refactoring
- enable the modernize linter and apply its fixes (d5eaff2)


## v0.34.1 - 2026-08-27
### Documentation
- correct the pages against the code (678d644)
- **agents:** refresh tech stack and widen the sync policy (c86d3e7)
- **cmd:** comma voice for the help surface (8e8dae0)
- **comparison:** comma voice for the closing paragraphs (24fd2b7)
- **contributing:** comma voice for the audit's new sentences (45e5855)
- **contributing:** correct the guide against the build reality (a530bc3)
- **man:** regenerate the man page for the diff help rewording (1e8d889)
- **readme:** license, contributing, and reference fixes (872a6f9)
- **style:** comma voice, drop the em-dashes (10826ce)

### Bug Fixes
- **cmd:** correct and clarify the help surface (e9ce309)


## v0.34.0 - 2026-08-27
### Documentation
- split the contributor guide out of the cookbook (ab4d39b)
- **agents:** the command catalogue spans docs pages, not one (76ba4cc)
- **agents:** make the docs cookbook the command catalogue (e55a993)
- **cmd:** reword the diff filter help (608da0e)
- **contributing:** carry the bare --version doc over the rebase (526c7a4)
- **drivers:** standardize the per-driver sections (3905793)
- **readme:** align the architecture prose with the how-it-works page (4fb19bf)
- **site:** restructure the site around per-topic pages (caba95d)
- **style:** let wide tables fit the center column (76250fd)

### Features
- **cmd:** print a bare version for --version (00a045d)

### Bug Fixes
- **cmd:** reject gron under --typed and stop swallowing the shorthand (169fbc2)
- **errors:** stop the redactor mangling iq's own unknown-source advice (8964899)
- **help:** stop the root example teaching --from/--combine (b9a3057)


## v0.33.0 - 2026-07-30
### Bug Fixes
- **cmd:** resolve a dotted address against the longest matching source (7403788)
- **combine:** refuse to plan a write the run cannot make (d1663b3)
- **combine:** make --insert --type take effect (37ceab7)
- **deps:** bump x/text and grpc to clear known vulnerabilities (dd91edb)
- **diff:** split --section on commas like inspect --only (f67ea0e)
- **query:** stop advising a --filter flag that does not exist (520fbd1)
- **query:** resolve the output format before the source in runJQ (9487c99)
- **root:** reject --insert/--typed alongside --from/--combine (32c631a)

### Features
- **add:** derive the source handle from the keyspace the url names (1ea4a18)
- **cmd:** complete enum flag values, lifecycle targets and inspect sections (708d295)
- **combine:** write the combined results with --insert (52d5d59)
- **combine:** promote the cross-source join to its own command (386dc47)
- **diff:** scope a diff or an inferred schema with a per-source filter (2a59797)
- **ping:** add --all to ping every saved source (496d468)

### Refactoring
- **cmd:** resolve every read source through one spec parser (71ae2b5)
- **query:** make Get omit a missing key instead of mapping it to nil (1f410c2)

### Documentation
- state the concrete pre-1.0 risks in the warning (7a38877)
- invert the site logo for dark mode, shrink the README mark to 60px (c9d3c7c)
- use the bold lens mark for the README and site logo (bb25882)
- add the iq logo to the README and the docs site (d0a6cb1)
- retitle pages, rename the exports guide, drop ./ from examples (24158d9)
- add a Mermaid query-routes diagram to Getting started (4d0f3a0)
- merge install into Getting started and add repo/theme metadata (199671f)
- split query-plan and diagnostics pages out of the Output page (80ac6b3)
- **site:** share the abbreviation definitions across every page (708b2f2)
- **site:** set site_url so instant previews work (05915eb)
- **site:** rename the task pages and reorder the nav (24bfa7d)


## v0.32.0 - 2026-07-22
### Documentation
- document the -v jq-stage annotation on the Output page (9d0cb45)
- move output, scans, and pushdown detail into docs-site pages (43913ab)
- merge Usage and Common commands into a categorized Getting started (936859b)
- drop the scans deep-dive and trim Drivers cross-references (59a55b9)
- render file dump formats as a Type/Description table (9b7eae2)
- restructure Drivers section with Database column and subheadings (331c23e)
- order the driver table alphabetically by name (3b7f773)
- render the driver list as a table with linked docs (89e47f4)
- trim README to an overview, defer detail to the docs site (658a014)
- add install section and docs-site install page (f11edc6)

### Features
- **cli:** add iq man page and dynamic shell completions (ccc660c)
- **explain:** annotate jq stages and mark the data-access route under -v (3289a31)


## v0.31.0 - 2026-07-21
### Documentation
- **site:** mirror the full README into the Zensical docs (86729e7)

### Bug Fixes
- **logging:** stop duplicate plan and record rendering across sinks (5e964be)

### Features
- **docs:** add zensical documentation site under docs/ (24e8596)
- **logging:** structured stage records, stream log targets, imply-enable (ed61322)


## v0.30.0 - 2026-07-20
### Features
- **diff:** color the human report by operation (065e877)
- **output:** color raw and gron renderings on a terminal (b709356)


## v0.29.0 - 2026-07-20
### Refactoring
- **diff:** fold the anchor predicate into one type switch (6b7a3ed)
- **diff:** bound the lcs backtrack loop (1a31225)

### Bug Fixes
- **deps:** bump apache/thrift to v0.23.0 for GHSA-wf45-q9ch-q8gh (57ec13b)

### Features
- **diff:** lcs array alignment, --set-arrays multiset mode, rfc 6902 --patch output (33aebef)

### Performance
- **mutation:** scope enumeration to changed packages and gate errored mutants (03edca0)


## v0.28.0 - 2026-07-19
### Documentation
- add export cookbook, null-vs-missing stance, pushdown citations (5f27006)
- **mutation:** committed baseline justification notes (5e279a0)

### Features
- **explain:** report per-conjunct pushdown decisions (2450885)
- **file:** prefilter uncached jsonl scans client-side with rawpred (e78e59f)
- **output:** add parquet export format (3eb15aa)
- **rawpred:** evaluate size and element predicates on raw bytes (c2eeb0f)
- **schema:** emit odcs v3.1.0 contracts (8951a02)


## v0.27.0 - 2026-07-18
### Features
- **couchbase:** prefilter residual scans client-side with rawpred (f5d398f)
- **elasticsearch:** prefilter residual scans client-side with rawpred (de39f9d)
- **rawpred:** evaluate portable regex predicates on raw bytes (e2ad0d0)
- **redis:** prefilter scans client-side with raw-byte predicate evaluation (3d134a6)

### Performance
- **elasticsearch:** reuse a prepared rawpred matcher per scan (1074b68)


## v0.26.0 - 2026-07-17
### Features
- **output:** add gron and grona renderings (a19ffe0)
- **schema:** emit draft 2020-12 json schemas (75ef7f9)


## v0.25.0 - 2026-07-17
### Features
- **schema:** infer draft-07 schemas and allow cross-driver --schema (26e5673)


## v0.24.0 - 2026-07-16
### Documentation
- **contract:** allocate host port 8086 to the planned bigtable driver (8ade248)
- **contract:** replace the bare-verb exec family with routed native expressions (f9b94d5)

### Bug Fixes
- **deps:** raise go directive to 1.26.5 to clear GO-2026-5856 (82f48e7)

### Features
- **couchbase:** add the Couchbase backend driver (228e866)
- **query:** add the typed Deleter port and iq data delete (8065cc9)


## v0.23.0 - 2026-07-08
### Features
- **cassandra:** count upsert overwrites via a batch key pre-read (a1e59dd)
- **couchdb:** push portable $regex and $size Mango selectors (7a68465)
- **drivers:** reject non-object put values instead of wrapping (44588f8)
- **dynamodb:** count upsert overwrites via a key-only batch pre-read (798d2f9)
- **hbase:** rename the ?rowkeytype= url param to ?keytype= (eef03a1)
- **hbase:** count upsert overwrites via existence-only point gets (5bf003b)
- **redis:** answer Estimator with DBSIZE for scan progress totals (86436f2)

### Bug Fixes
- **cli:** apply per-source stored options to a collection-addressed --src (7b8fd7a)
- **cmd:** keep prose intact when redacting a URL in an error message (fd85f44)
- **pushdown:** match pushed regex flags and escapes to gojq semantics (c0d6086)
- **pushdown:** reject $-prefixed field names before they reach a query (c57eef6)
- **pushdown:** reject widened negation to stop dropping matching rows (5aa0632)

### Documentation
- codify the cross-driver contract and align AGENTS.md and README (85b80c6)
- add driver-adoption gate to AGENTS.md (3b8b29e)
- note the log-option/env precedence and source() buffering (24f4405)

### Refactoring
- **cmd:** extract the shared inspect render tail (07c4961)
- **numfmt:** hoist JSON number conversion out of three drivers (a4d9122)
- **query:** detect source() by compiling, not matching gojq error text (58bd25e)


## v0.22.0 - 2026-07-07
### Features
- **driver:** show the copy-pasteable ?format=<name> in driver ls -v (8a3b869)
- **driver:** compact the file dump-format catalogue behind driver ls -v (587eb6f)


## v0.21.1 - 2026-07-07
### Bug Fixes
- **deps:** patch known advisories in golang.org/x/net and go-pkcs12 (671d1bb)
- **hbase:** reject int values outside int32 range on write (36bcc7e)


## v0.21.0 - 2026-07-07
### Features
- **elasticsearch:** add Elasticsearch driver (ca062b7)
- **opensearch:** add OpenSearch as a shared driver flavor (3938620)

### Documentation
- **readme:** note the Neo4j dump id vs live elementId difference (70864ae)


## v0.20.0 - 2026-07-07
### Features
- **file:** read Neo4j APOC JSON export offline (cd23a36)
- **neo4j:** read relationship-type collections (09e5f78)
- **neo4j:** add Neo4j property-graph driver (0760fdf)


## v0.19.0 - 2026-07-06
### Features
- **couchdb:** add Apache CouchDB driver (5a60810)
- **file:** read Cassandra native dumps (56a9a7c)
- **file:** read DynamoDB native dumps (489c625)

### Documentation
- trim redundant book intros, tighten worktree rule (7492a7f)


## v0.18.0 - 2026-07-06
### Features
- **cmd:** show handle/driver/url in iq ls with sq-style color (beb8f1c)
- **hbase:** add Apache HBase driver (cee861f)

### Bug Fixes
- **cmd:** drop redundant collection tag from iq ls table (0196d2a)


## v0.17.0 - 2026-07-06
### Features
- **dynamodb:** add Amazon DynamoDB driver (177ef0c)


## v0.16.0 - 2026-07-06
### Features
- **cassandra:** add Apache Cassandra driver (f84cf0d)


## v0.15.0 - 2026-07-06
### Features
- **cli:** address collections as handle.collection; drivers own url params (4bb5a9b)


## v0.14.0 - 2026-07-06
### Features
- **file:** cache decoded file:// dumps with a per-page key index (74e6195)


## v0.13.0 - 2026-07-06
### Features
- **cli:** align command/flag surface with sq (3b02e2c)

### Documentation
- **readme:** compact architecture diagrams and make them backend-agnostic (071f47c)


## v0.12.0 - 2026-07-06
### Documentation
- **readme:** mark file:// in comparison table, split architecture diagram (0f84849)
- **readme:** add more tools to the comparison table (1497963)
- **readme:** trim redundant phrase from comparison intro (5c0d419)
- **readme:** add beta / not-production-ready warning (abb0656)
- **readme:** group Redis and MongoDB under a collapsible Drivers section (adb0449)

### Features
- **cli:** replace `iq data copy` with sq-style --insert/--typed movement (d794e09)
- **cli:** group top-level commands in --help (9e97e97)
- **cli:** show supported backend versions in `iq driver ls` (a3554de)
- **cli:** reshape `iq add` to match `sq add` (ae99fad)
- **cli:** add unified --format selector and --format.decimal control (b0167c8)
- **cmd:** add -o/--output flag to write output to a file (60e45d4)
- **cmd:** tint verbose log lines like sq (1174ffe)
- **config:** add config command and --config flag with per-source option defaults (4b4fa45)
- **data:** add iq data movement and lifecycle commands (d1c71a6)
- **file:** add read-only file:// source for querying database dumps (2e52f6a)
- **progress:** show a cheap backend estimate beside the scanned count (6bd9f7a)

### Refactoring
- **cli:** rename --dry-run to --explain (3d8653e)


## v0.11.0 - 2026-07-05
### Features
- **cmd:** add --dry-run query plan and verbose backend command trace (9d0005f)


## v0.10.0 - 2026-07-05
### Features
- **cli:** default MongoDB pushdown on, replace --compile with --no-compile (3ec8581)
- **cli:** group --help flags into logical sections (c909def)
- **cmd:** add sq-style verbose, logging, error, and pprof flags (95681b5)
- **cmd:** replace --format/-o with per-format boolean flags (c2fd693)
- **cmd:** make inspect take a source positional, move narrowing to --only (8b0243a)
- **driver:** add driver registry and `iq driver ls` command (5ff8ece)

### Documentation
- **readme:** link comparison tools, drop backticks from iq/sq (9c0042c)
- **readme:** add tool comparison and awesome-jq reference (deb0c79)


## v0.9.0 - 2026-07-05
### Features
- **cmd:** add --reveal and --expand flags to ls and inspect (9fb0247)
- **cmd:** colorize output on a terminal (18ded16)
- **progress:** add scan progress spinner for unbounded queries (add320c)

### Refactoring
- **backend:** move mongo and redis adapters under internal/backend (cae524a)


## v0.8.0 - 2026-07-05
### Features
- **cmd:** add diff for comparing two sources (b0d01b8)

### Documentation
- **readme:** add collapsed Redis section, fold MongoDB section (9dfe81c)
- **readme:** add core-pipeline mermaid diagram and sync doctrine (6fa4d65)


## v0.7.0 - 2026-07-05
### Features
- **cmd:** add --compact output flag (a3f659a)
- **source:** list inspect subcommands per driver (3095759)


## v0.6.0 - 2026-07-05
### Features
- **output:** add --format/-o with json, jsonl, json-array, values, yaml (4c20f80)
- **source:** add inspect for native database introspection (dfbfd07)
- **source:** add ping to check source reachability (2a0d12c)
- **source:** add ls --reveal for unredacted URLs (a56a3c3)
- **source:** add ls --verbose, --group, --json and group filter (15ffe15)
- **source:** remove multiple sources and groups in one rm (0b00977)
- **source:** add mv to rename and move sources and groups (3d9d77f)
- **source:** store credentials in the OS keyring (342f4b8)

### Bug Fixes
- **release:** degrade changelog gen gracefully behind a published tag (ba0541a)


## v0.5.0 - 2026-07-04
### Bug Fixes
- **release:** ignore untracked files in the clean-tree guard (f54c665)
- **release:** generate changelog before the first tag exists (1b02753)

### Features
- **release:** add changelog automation and version reporting (d8ce677)


## v0.4.0 - 2026-07-04
### Features
- **cmd:** add cross-source queries via --from/--combine (73977e8)
- **cmd:** add saved sources with active source and group (d2ba7bc)
- **query:** add in-filter source() for composable cross-source queries (de8d919)


## v0.3.0 - 2026-07-04
### Features
- push exact negation via $ne/$exists/$not $elemMatch (--compile) (78b7c17)
- push .a | any(cond) to $elemMatch (--compile) (77928b8)
- push has() to $exists and length == n to $size (--compile) (77ad270)
- push portable regex and collapse equality-or to $in (--compile) (54750ec)
- push range comparisons to MongoDB via jq-type-order supersets (8cbccf3)
- compile .[]|select equality predicates to MongoDB queries (--compile) (2c2e25c)

### Documentation
- link book files and tidy tech-stack bullet in AGENTS.md (f2e528e)
- list --compile pushdown capabilities in iq --help (70f93f3)
- add a --compile capabilities table to the README (2a37277)


## v0.2.0 - 2026-07-04
### Features
- add MongoDB as a second backend behind the jq interface (5424003)


## v0.1.0 - 2026-07-04
### Features
- stream .[]-rooted scans in constant memory (39b648a)
- query redis with jq filters as the default action (a21555e)
- render replies in redis-cli style; enforce mutation gate via wrapper (0cfba8d)
- add redis query-forwarding cli with mutation gate (8e8ca45)

### Documentation
- add AGENTS.md doctrine and distilled guideline books (16e247b)


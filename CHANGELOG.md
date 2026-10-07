# CHANGELOG

All notable changes to this project are documented here.
This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
and its commits follow [Conventional Commits](https://www.conventionalcommits.org/).

## v0.38.2 - 2026-10-07
### Bug Fixes
- **cassandra:** split the userinfo at the last @ and decode it (GHSA-67pr-hcv5-v9hp) (#93) (aef36b8)
- **couchbase:** do not push a field name that holds a backslash (GHSA-q2ww-49v8-p2r5) (#96) (b9fe9ab)
- **deps:** use the zsltg/rdb fork to stop a fatal OOM on a crafted RDB (GHSA-v26p-9927-qf6x) (#98) (1230c4f)
- **file:** read back every record that the decode cache writes (GHSA-j9g3-4x25-72vp) (#95) (bec1feb)
- **redis:** return a fixed error when the connection URI does not parse (GHSA-hj6f-gr69-77vj) (#94) (f3b6010)

### Documentation
- add a security index for agents (#90) (3329727)


## v0.38.1 - 2026-10-06
### Bug Fixes
- **capabilities:** refuse non-linux baseline updates from the host target (#71) (65bd8e3)
- **cmd:** redact the connection password in log records and errors (GHSA-46m6-qx4m-5xj6) (#75) (4a7f98e)
- **file:** keep the sign of a negative zero in the prefiltered scan (#52) (dea4b08)
- **hbase:** stop Open at the caller's deadline (#58) (3f199d1)
- **mcp:** refuse a filter in a side spec on the stats layer (#50) (9b8534d)
- **pushdown:** refuse a pushdown that a user definition shadows (#60) (ffe19ac)
- **rawpred:** keep records whose bytes decode to U+FFFD (#62) (21e3ee6)

### Documentation
- replace the logo and favicon (#74) (86b2884)
- **agents:** separate core rules from task procedures (#65) (d0680cf)

### Refactoring
- raise the code health of the diff, inspect and ls commands (#55) (6c86e1a)
- raise the code health of the elasticsearch driver (#59) (90ea2f2)
- raise the code health of the hbase driver (#61) (e2900ea)
- raise the code health of the file driver and the query core (#56) (db25583)
- raise the code health of the pure query packages (#53) (fdb8dca)
- raise the code health of pushdown, inspect and diff (#49) (809b77d)


## v0.38.0 - 2026-09-30
### Bug Fixes
- **dynamodb:** parse integers at the size of int (#41) (854af85)
- **pushdown:** push only the selects that test the element itself (#44) (9c1f2c5)

### Features
- **add:** store a source password in the os keyring by default (#36) (5530e8c)


## v0.37.1 - 2026-09-29
### Bug Fixes
- keep the sign of a negative zero across a typed dump (#32) (37d9838)
- fix four bugs found by new go native fuzz targets (#27) (ad220e0)

### Documentation
- drop the ai assistance section from the pull request template (#26) (7d63a31)
- split contributing into a short guide and a development guide (#24) (538d468)
- explain how a pull request from a fork runs (#23) (58481ec)
- require an answer to every review thread before the merge (#21) (9fc9062)


## v0.37.0 - 2026-09-28
### Documentation
- link the coding standard, the security policy and the test policy (#19) (9fc09f1)
- state the test policy in contributing (#12) (ad44704)
- add a review guide for all reviewers (#9) (a3708d9)
- name iq exec as a write path in the note (#8) (758c007)
- describe the pull request merge flow (#4) (4f12712)
- **mcp:** say that --allow exec gives the database account's reach (#10) (4a5dda7)
- **readme:** put each badge cluster on its own row (#18) (a210638)
- **readme:** add the openssf best practices badge (#16) (e36d804)
- **readme:** add the codescene code health badge (#14) (6ad1a2c)

### Features
- **config:** warn when other users can read an inline password (#11) (0829d56)

### Bug Fixes
- **deps:** bump grpc to v1.83.2 and x/crypto to v0.56.0 (519b39e)
- **hooks:** skip published commits in pre-push (#6) (f566ec9)


## v0.36.0 - 2026-09-27
### Documentation
- split long sentences in the architecture text (0b4f028)
- remove the Comparison link from the See also lists (236d652)
- state the pushdown coverage per backend correctly (c1425c1)
- fix three splits that moved a referent or a cause (000ebcd)
- split long sentences on the small reference pages (a309340)
- apply the STE word and punctuation rules to the docs site (2503b7e)
- say what a bypassed prefilter saves instead of calling it useless (aece387)
- split long sentences in the README head and the Get started page (ff631c9)
- split long sentences in the Guarantees lists (b256980)
- **agents:** split long sentences on the AI agents page (81487e4)
- **agents:** apply the STE word and punctuation rules to AGENTS.md (7710e24)
- **comparison:** split long sentences on the Comparison page (2f862b1)
- **contributing:** state the policy for AI-assisted contributions (dedfeb9)
- **contributing:** record how to harden a package's mutation debt (b3ce953)
- **demo:** re-record after the jqfmt printer refactor (84ad4d5)
- **demo:** re-record the demo after the comment rewrite (3cb0ff4)
- **drivers:** split long sentences and chains on the Drivers page (bf1f155)
- **loading-exports:** split long sentences on the Loading exports page (0411fe0)
- **mutation:** describe the hardened weekly scan and its limits (8fa255a)
- **mutation:** do not confirm a kill with the single-mutant diagnostic (7fd68b4)
- **output:** split long sentences on the Output page (3367eb6)
- **query-data:** split long sentences on the Query data page (7ffe334)
- **readme:** apply the STE word and punctuation rules to the README and its copies (3f819b4)
- **readme:** hide the mutation badge until the scan can publish (dcd3e07)
- **readme:** split long sentences and chains in the rest of the README (277d821)
- **readme:** cut the badge row to eight in three clusters (2cab44b)
- **sources:** split long sentences on the Sources page (ae89b91)
- **write-data:** split long sentences on the Write data page (8eea4c3)

### Bug Fixes
- **couchbase:** stop a cancelled context from completing a KV batch (f4bea34)
- **deps:** bump grpc and thrift past their advisories (f677b8d)
- **file:** accept the file:///C:/ drive form and build test urls portably (23ad9ed)
- **mutation:** mark failed packages so an old pass cannot hide them (84c80d0)
- **mutation:** close false greens in the verdict, the plan and the fingerprint (6b04236)
- **mutation:** score killed over killed plus escaped in the summary (6df8ac4)
- **mutation:** refuse parallel workers on a shared backend (771acb5)
- **scripts:** keep local worktrees out of the osv-scanner scan (804e0b8)
- **scripts:** make the mutation dry run read .mutago.yml (e6eaaf3)
- **scripts:** make the couchbase seed fail when it writes nothing (e0df9c7)

### Features
- **mutation:** add measured starting rates for five more drivers (5c1a4a6)
- **mutation:** start the shard plan from measured rates (ed06071)
- **mutation:** add the scripts of the incremental sharded scan (a78366c)
- **mutation:** add shard mode and file targets to the gate wrapper (faedc6f)

### Refactoring
- **couchbase:** move the scan page read into readPage (8b0aff6)
- **jqfmt:** let clause presence decide the if break (844e89d)


## v0.35.0 - 2026-08-30
### Documentation
- use the comma voice, break up long paragraphs and list the capabilities (35361d3)
- tighten the readme and the docs landing page (08bac9f)
- describe the two url-only footnotes (1d53abd)
- lead with what iq does, one tagline pair, compare by job (ff833ab)
- **agents:** show the mcp server registration for every major harness (8e3f4cc)
- **demo:** frame the recording as a terminal window (d3a289d)
- **flags:** document each flag once and mark the global ones (6a700dc)
- **index:** drop the Go footnote (4a7d128)
- **index:** drop the NoSQL footnote (e6bdf71)
- **index:** footnote ast, pushdown, port and keyring (3d5cb34)
- **readme:** show a diff of one document across two sources (2e83ec6)
- **readme:** introduce each get-started example with a plain sentence (50f17c6)
- **readme:** rework get started into labelled one-liners by job (3d52125)
- **readme:** make every get-started example copyable (322ee46)
- **readme:** adopt the residua badge set (8a3fbc0)
- **readme:** point the intro at the drivers section (3cf78a1)
- **readme:** point each get-started section at its docs page (235d4a0)
- **readme:** lead with the demo and fold the agent setup into install (1fe7ad9)
- **readme:** inline the collapsed-section headings and drop the tail sections (c7b5be8)
- **readme:** keep the query-cost paragraph under query routes (3ab9380)
- **readme:** show a filtered copy under --insert (7590adf)
- **readme:** trim the query-cost paragraph to its own facts (ecbf693)
- **see-also:** flatten the link list to one bullet each (dc850be)
- **see-also:** link sq (82170cd)
- **write-data:** describe --type and --replace, add lifecycle preview examples (24fbda6)
- **write-data:** split the type and replace prose into shorter paragraphs (5d51eca)

### Features
- **demo:** record the README demo and gate its freshness (83a8b44)
- **mcp:** serve the query core as an MCP server over stdio (9ffbba4)
- **skill:** ship the agent skill and the one-file manual (9ee582e)


## v0.34.2 - 2026-08-27
### Documentation
- replace the pre-1.0 warning with an ai-assistance note (f4aa249)
- **see-also:** map the jq ecosystem (301206a)

### Bug Fixes
- **deps:** bump go-archive, otel and x/net past their advisories (b2ae143)

### Refactoring
- enable the modernize linter and apply its fixes (dccd747)


## v0.34.1 - 2026-08-27
### Documentation
- correct the pages against the code (08ff38b)
- **agents:** refresh tech stack and widen the sync policy (98c6583)
- **cmd:** comma voice for the help surface (c1bc4b8)
- **comparison:** comma voice for the closing paragraphs (8859e9c)
- **contributing:** comma voice for the audit's new sentences (acdaa95)
- **contributing:** correct the guide against the build reality (9f35b44)
- **man:** regenerate the man page for the diff help rewording (23c9666)
- **readme:** license, contributing, and reference fixes (ff6891a)
- **style:** comma voice, drop the em-dashes (f162720)

### Bug Fixes
- **cmd:** correct and clarify the help surface (ca26945)


## v0.34.0 - 2026-08-27
### Documentation
- split the contributor guide out of the cookbook (4416944)
- **agents:** the command catalogue spans docs pages, not one (83602b4)
- **agents:** make the docs cookbook the command catalogue (046e2b2)
- **cmd:** reword the diff filter help (bece9b2)
- **contributing:** carry the bare --version doc over the rebase (462f660)
- **drivers:** standardize the per-driver sections (3507837)
- **readme:** align the architecture prose with the how-it-works page (dba169e)
- **site:** restructure the site around per-topic pages (a22911d)
- **style:** let wide tables fit the center column (68a0d4b)

### Features
- **cmd:** print a bare version for --version (2e2aa36)

### Bug Fixes
- **cmd:** reject gron under --typed and stop swallowing the shorthand (ef416f1)
- **errors:** stop the redactor mangling iq's own unknown-source advice (7d35a6d)
- **help:** stop the root example teaching --from/--combine (84b0c9a)


## v0.33.0 - 2026-07-30
### Bug Fixes
- **cmd:** resolve a dotted address against the longest matching source (b122d52)
- **combine:** refuse to plan a write the run cannot make (863eb8e)
- **combine:** make --insert --type take effect (2e6451a)
- **deps:** bump x/text and grpc to clear known vulnerabilities (11bced9)
- **diff:** split --section on commas like inspect --only (483930d)
- **query:** stop advising a --filter flag that does not exist (53708d4)
- **query:** resolve the output format before the source in runJQ (3f1def7)
- **root:** reject --insert/--typed alongside --from/--combine (02eb65b)

### Features
- **add:** derive the source handle from the keyspace the url names (1d5f74e)
- **cmd:** complete enum flag values, lifecycle targets and inspect sections (3ec3f0e)
- **combine:** write the combined results with --insert (e4c88a0)
- **combine:** promote the cross-source join to its own command (dcfd84e)
- **diff:** scope a diff or an inferred schema with a per-source filter (fb4dcea)
- **ping:** add --all to ping every saved source (d704c30)

### Refactoring
- **cmd:** resolve every read source through one spec parser (22784ed)
- **query:** make Get omit a missing key instead of mapping it to nil (1ef2b70)

### Documentation
- state the concrete pre-1.0 risks in the warning (b9f966f)
- invert the site logo for dark mode, shrink the README mark to 60px (cbc77e0)
- use the bold lens mark for the README and site logo (bada991)
- add the iq logo to the README and the docs site (edd7e57)
- retitle pages, rename the exports guide, drop ./ from examples (d3a8258)
- add a Mermaid query-routes diagram to Getting started (423071e)
- merge install into Getting started and add repo/theme metadata (5856bfb)
- split query-plan and diagnostics pages out of the Output page (13ccdaa)
- **site:** share the abbreviation definitions across every page (92cf891)
- **site:** set site_url so instant previews work (d535324)
- **site:** rename the task pages and reorder the nav (b191fdd)


## v0.32.0 - 2026-07-22
### Documentation
- document the -v jq-stage annotation on the Output page (fedc366)
- move output, scans, and pushdown detail into docs-site pages (9dd4c07)
- merge Usage and Common commands into a categorized Getting started (85a2b57)
- drop the scans deep-dive and trim Drivers cross-references (2b7f8ee)
- render file dump formats as a Type/Description table (67082e8)
- restructure Drivers section with Database column and subheadings (606fe42)
- order the driver table alphabetically by name (c7f9a1d)
- render the driver list as a table with linked docs (f80f56e)
- trim README to an overview, defer detail to the docs site (8cb4450)
- add install section and docs-site install page (244edae)

### Features
- **cli:** add iq man page and dynamic shell completions (be4b578)
- **explain:** annotate jq stages and mark the data-access route under -v (505fef0)


## v0.31.0 - 2026-07-21
### Documentation
- **site:** mirror the full README into the Zensical docs (03268e4)

### Bug Fixes
- **logging:** stop duplicate plan and record rendering across sinks (3adf5e7)

### Features
- **docs:** add zensical documentation site under docs/ (7dd1361)
- **logging:** structured stage records, stream log targets, imply-enable (6b8bdf7)


## v0.30.0 - 2026-07-20
### Features
- **diff:** color the human report by operation (e691594)
- **output:** color raw and gron renderings on a terminal (a5d71c4)


## v0.29.0 - 2026-07-20
### Refactoring
- **diff:** fold the anchor predicate into one type switch (a150d58)
- **diff:** bound the lcs backtrack loop (2dafcff)

### Bug Fixes
- **deps:** bump apache/thrift to v0.23.0 for GHSA-wf45-q9ch-q8gh (66f89bf)

### Features
- **diff:** lcs array alignment, --set-arrays multiset mode, rfc 6902 --patch output (de6e1f5)

### Performance
- **mutation:** scope enumeration to changed packages and gate errored mutants (be739f3)


## v0.28.0 - 2026-07-19
### Documentation
- add export cookbook, null-vs-missing stance, pushdown citations (503a9a6)
- **mutation:** committed baseline justification notes (46b098c)

### Features
- **explain:** report per-conjunct pushdown decisions (fddd6d4)
- **file:** prefilter uncached jsonl scans client-side with rawpred (964a72b)
- **output:** add parquet export format (8e9c58b)
- **rawpred:** evaluate size and element predicates on raw bytes (c5f550b)
- **schema:** emit odcs v3.1.0 contracts (24c0f5a)


## v0.27.0 - 2026-07-18
### Features
- **couchbase:** prefilter residual scans client-side with rawpred (6364567)
- **elasticsearch:** prefilter residual scans client-side with rawpred (f91a505)
- **rawpred:** evaluate portable regex predicates on raw bytes (fccd259)
- **redis:** prefilter scans client-side with raw-byte predicate evaluation (ed9f6ed)

### Performance
- **elasticsearch:** reuse a prepared rawpred matcher per scan (5e95c32)


## v0.26.0 - 2026-07-17
### Features
- **output:** add gron and grona renderings (8d091c9)
- **schema:** emit draft 2020-12 json schemas (0addcb8)


## v0.25.0 - 2026-07-17
### Features
- **schema:** infer draft-07 schemas and allow cross-driver --schema (18a83e6)


## v0.24.0 - 2026-07-16
### Documentation
- **contract:** allocate host port 8086 to the planned bigtable driver (8c4e000)
- **contract:** replace the bare-verb exec family with routed native expressions (e9eaab6)

### Bug Fixes
- **deps:** raise go directive to 1.26.5 to clear GO-2026-5856 (1fe5ec3)

### Features
- **couchbase:** add the Couchbase backend driver (c4d7108)
- **query:** add the typed Deleter port and iq data delete (11d2361)


## v0.23.0 - 2026-07-08
### Features
- **cassandra:** count upsert overwrites via a batch key pre-read (ace4c75)
- **couchdb:** push portable $regex and $size Mango selectors (ae6780d)
- **drivers:** reject non-object put values instead of wrapping (d180d92)
- **dynamodb:** count upsert overwrites via a key-only batch pre-read (7c66e94)
- **hbase:** rename the ?rowkeytype= url param to ?keytype= (f1ec5f4)
- **hbase:** count upsert overwrites via existence-only point gets (f0a018d)
- **redis:** answer Estimator with DBSIZE for scan progress totals (dfe6673)

### Bug Fixes
- **cli:** apply per-source stored options to a collection-addressed --src (0ea7d86)
- **cmd:** keep prose intact when redacting a URL in an error message (23e1540)
- **pushdown:** match pushed regex flags and escapes to gojq semantics (5293286)
- **pushdown:** reject $-prefixed field names before they reach a query (9b41f97)
- **pushdown:** reject widened negation to stop dropping matching rows (51888c2)

### Documentation
- codify the cross-driver contract and align AGENTS.md and README (8c294b6)
- add driver-adoption gate to AGENTS.md (4b15128)
- note the log-option/env precedence and source() buffering (e11af55)

### Refactoring
- **cmd:** extract the shared inspect render tail (980a70d)
- **numfmt:** hoist JSON number conversion out of three drivers (8e3497e)
- **query:** detect source() by compiling, not matching gojq error text (6f89540)


## v0.22.0 - 2026-07-07
### Features
- **driver:** show the copy-pasteable ?format=<name> in driver ls -v (b4eca98)
- **driver:** compact the file dump-format catalogue behind driver ls -v (bd21d98)


## v0.21.1 - 2026-07-07
### Bug Fixes
- **deps:** patch known advisories in golang.org/x/net and go-pkcs12 (c07d1dd)
- **hbase:** reject int values outside int32 range on write (76c9297)


## v0.21.0 - 2026-07-07
### Features
- **elasticsearch:** add Elasticsearch driver (28033e8)
- **opensearch:** add OpenSearch as a shared driver flavor (fcd427d)

### Documentation
- **readme:** note the Neo4j dump id vs live elementId difference (d428de1)


## v0.20.0 - 2026-07-07
### Features
- **file:** read Neo4j APOC JSON export offline (550f9a5)
- **neo4j:** read relationship-type collections (edf07cb)
- **neo4j:** add Neo4j property-graph driver (30b8398)


## v0.19.0 - 2026-07-06
### Features
- **couchdb:** add Apache CouchDB driver (5a60810)
- **file:** read Cassandra native dumps (fe816a1)
- **file:** read DynamoDB native dumps (f4a7ea2)

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


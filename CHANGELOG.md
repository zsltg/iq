# CHANGELOG

All notable changes to this project are documented here.
This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
and its commits follow [Conventional Commits](https://www.conventionalcommits.org/).

## v0.10.0 - 2026-07-05
### Features
- **cli:** default MongoDB pushdown on, replace --compile with --no-compile (fcc38e3)
- **cli:** group --help flags into logical sections (5a72dfa)
- **cmd:** add sq-style verbose, logging, error, and pprof flags (0f68c5c)
- **cmd:** replace --format/-o with per-format boolean flags (289176f)
- **cmd:** make inspect take a source positional, move narrowing to --only (fdb0f35)
- **driver:** add driver registry and `iq driver ls` command (25a6c3f)

### Documentation
- **readme:** link comparison tools, drop backticks from iq/sq (ca88124)
- **readme:** add tool comparison and awesome-jq reference (335e05a)


## v0.9.0 - 2026-07-05
### Features
- **cmd:** add --reveal and --expand flags to ls and inspect (1fc7305)
- **cmd:** colorize output on a terminal (9114870)
- **progress:** add scan progress spinner for unbounded queries (2428de0)

### Refactoring
- **backend:** move mongo and redis adapters under internal/backend (3f4ac31)


## v0.8.0 - 2026-07-05
### Features
- **cmd:** add diff for comparing two sources (a3d4c49)

### Documentation
- **readme:** add collapsed Redis section, fold MongoDB section (ca6da68)
- **readme:** add core-pipeline mermaid diagram and sync doctrine (c380f3c)


## v0.7.0 - 2026-07-05
### Features
- **cmd:** add --compact output flag (ec67e92)
- **source:** list inspect subcommands per driver (a95276a)


## v0.6.0 - 2026-07-05
### Features
- **output:** add --format/-o with json, jsonl, json-array, values, yaml (03188a7)
- **source:** add inspect for native database introspection (60d785b)
- **source:** add ping to check source reachability (3e37d51)
- **source:** add ls --reveal for unredacted URLs (8becb3b)
- **source:** add ls --verbose, --group, --json and group filter (7a5731d)
- **source:** remove multiple sources and groups in one rm (6ea9705)
- **source:** add mv to rename and move sources and groups (2cc6cca)
- **source:** store credentials in the OS keyring (1e45e2e)

### Bug Fixes
- **release:** degrade changelog gen gracefully behind a published tag (8545b81)


## v0.5.0 - 2026-07-04
### Bug Fixes
- **release:** ignore untracked files in the clean-tree guard (075a3a6)
- **release:** generate changelog before the first tag exists (d548482)

### Features
- **release:** add changelog automation and version reporting (b21bb20)


## v0.4.0 - 2026-07-04
### Features
- **cmd:** add cross-source queries via --from/--combine (010d61f)
- **cmd:** add saved sources with active source and group (2b842a2)
- **query:** add in-filter source() for composable cross-source queries (47129b5)


## v0.3.0 - 2026-07-04
### Features
- push exact negation via $ne/$exists/$not $elemMatch (--compile) (21fcf9e)
- push .a | any(cond) to $elemMatch (--compile) (4a8b1ed)
- push has() to $exists and length == n to $size (--compile) (75806ea)
- push portable regex and collapse equality-or to $in (--compile) (a9cb0f4)
- push range comparisons to MongoDB via jq-type-order supersets (ae606b1)
- compile .[]|select equality predicates to MongoDB queries (--compile) (ddf62e5)

### Documentation
- link book files and tidy tech-stack bullet in AGENTS.md (964f7fb)
- list --compile pushdown capabilities in iq --help (ad2e9b0)
- add a --compile capabilities table to the README (cc94e56)


## v0.2.0 - 2026-07-04
### Features
- add MongoDB as a second backend behind the jq interface (c5c315f)


## v0.1.0 - 2026-07-04
### Features
- stream .[]-rooted scans in constant memory (7f470d4)
- query redis with jq filters as the default action (4fbc7cc)
- render replies in redis-cli style; enforce mutation gate via wrapper (c88f928)
- add redis query-forwarding cli with mutation gate (cefb046)

### Documentation
- add AGENTS.md doctrine and distilled guideline books (8d54328)


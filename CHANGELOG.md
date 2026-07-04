# CHANGELOG

All notable changes to this project are documented here.
This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
and its commits follow [Conventional Commits](https://www.conventionalcommits.org/).

## v0.1.0 - 2026-07-04
### Features
- push portable regex and collapse equality-or to $in (--compile) (a9cb0f4)
- render replies in redis-cli style; enforce mutation gate via wrapper (c88f928)
- add redis query-forwarding cli with mutation gate (cefb046)
- push exact negation via $ne/$exists/$not $elemMatch (--compile) (21fcf9e)
- push .a | any(cond) to $elemMatch (--compile) (4a8b1ed)
- push has() to $exists and length == n to $size (--compile) (75806ea)
- compile .[]|select equality predicates to MongoDB queries (--compile) (ddf62e5)
- push range comparisons to MongoDB via jq-type-order supersets (ae606b1)
- query redis with jq filters as the default action (4fbc7cc)
- add MongoDB as a second backend behind the jq interface (c5c315f)
- stream .[]-rooted scans in constant memory (7f470d4)
- **cmd:** add cross-source queries via --from/--combine (010d61f)
- **cmd:** add saved sources with active source and group (2b842a2)
- **query:** add in-filter source() for composable cross-source queries (47129b5)

### Documentation
- link book files and tidy tech-stack bullet in AGENTS.md (964f7fb)
- list --compile pushdown capabilities in iq --help (ad2e9b0)
- add a --compile capabilities table to the README (cc94e56)
- add AGENTS.md doctrine and distilled guideline books (8d54328)


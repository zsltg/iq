---
icon: material/link-variant
---

# See also

`iq` uses jq as its filter language, so the jq ecosystem carries over.

## The jq language

- [jq manual](https://jqlang.org/manual/): the language reference for the
  filters `iq` runs.
- [awesome-jq](https://github.com/jqlang/awesome-jq): the curated list of jq
  tools, guides, and resources.

## jq engines

- [gojq](https://github.com/itchyny/gojq): the pure-Go jq implementation
  `iq` embeds, so filter semantics here are gojq's.
- [jaq](https://github.com/01mf02/jaq): a Rust jq clone focused on speed and
  stricter semantics.

## jq for other data

- [yq](https://github.com/mikefarah/yq): jq-style filters for YAML, TOML,
  and XML.
- [fq](https://github.com/wader/fq): jq for binary formats, inspect a file
  the way `iq` inspects a database.
- [jc](https://github.com/kellyjonbrazil/jc): converts classic CLI output to
  JSON, so any command becomes jq (or piped `iq`) input.

## Interactive jq

- [jqp](https://github.com/noahgorstein/jqp): a TUI playground that
  live-previews a filter as you type, handy for building `iq` filters.
- [ijq](https://github.com/gpanders/ijq): interactive jq with a side-by-side
  input and output view.

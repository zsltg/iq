# Usage

*This page mirrors the project [README](https://github.com/zsltg/iq/blob/main/README.md), which
remains the source of truth until the documentation is fully migrated.*

The default action is a jq filter. Its top-level paths name the keys to fetch; the result is
printed as pretty JSON by default (see [Output formats](output.md) to change it; these run
against the active source — see [Sources](sources.md)):

```bash
iq '.greeting'                          # fetch key "greeting"
iq '.["book:1"]'                        # a key containing a colon needs bracket-quoting
iq '.["book:1"].title'                  # fetch book:1, extract one field
iq '[ .["book:1"].title, .["book:2"].title ]'   # fetch both keys, project a field from each
iq '.["book:2"].price | tonumber + 5'   # values are strings; convert before arithmetic
```

Always wrap the filter in single quotes. jq syntax is full of characters the shell would
otherwise expand or split — brackets (`[ ]`), whitespace, `|`, `*`, `$` — and bracket-quoting a
colon key like `.["book:1"]` reads as a glob to zsh (`no matches found`) or bash unless quoted.

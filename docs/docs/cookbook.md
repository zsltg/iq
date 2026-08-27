---
icon: material/book-open-blank-variant-outline
---

# Cookbook

`iq` renders JSON-family formats only, no CSV and no tables, by design, its
output is meant to compose, so you can pipe it into other tools.

## Piped input

```sh { title='Implicit stdin source' }
cat dump.jsonl | iq '.[]'
```

Stdin becomes the source only when nothing else resolves, an active source wins
over the pipe.

`--from-format` forces the decode when the content cannot be sniffed (a gzipped
or oddly-shaped dump).

## sq

[`sq`](https://sq.io) is a command-line tool giving jq-style access to SQL
databases and files like CSV or Excel.

### SQL → NoSQL

```sh { title='Load a table from sq to iq' }
sq -J @pg.actor | iq --insert cache --key-field actor_id
```

`sq` emits one JSON object per row (`-J`), `iq` keys each by the named field,
foreign JSON always needs `--key-field` or `--key` (see
[Key mapping](write-data.md#key-mapping-key)).

### NoSQL → SQL

```sh { title='Load documents from iq to sq' }
iq --src books '.[]' --jsonl | sq --insert @dw.books .data
```

Piped data is sq's `.data` table, and the destination table is created if
missing.

### CSV or Excel

```sh { title='Render data into CSV or Excel with sq' }
iq --src books '.[]' --jsonl | sq -C .data
iq --src books '.[]' --jsonl | sq -x .data -o books.xlsx
```

`iq` itself renders no CSV, sq is the tabular bridge.

## quicktype

[`quicktype`](https://github.com/glideapps/quicktype) generates strongly-typed
models and serializers from JSON, JSON Schema, TypeScript, and GraphQL queries.

```sh { title='Create typed models with quicktype from live documents' }
iq --src books '.[]' --jsona | quicktype -l typescript --top-level Book -o book.ts
```

The schema-fed variant (`iq schema … | quicktype -s schema`, see
[Schema](sources.md#schema-schema)) types the whole inferred shape, this one
types what a query actually returned, pipe a bounded result, not a scan of a
huge keyspace.

## gron

[`gron`](https://github.com/tomnomnom/gron) transforms JSON into discrete
assignments to make it easier to grep for what you want and see the absolute
path to it.

```sh { title='Grep the flattened view then unflatten it' }
iq --src books '.[]' -G | grep -i title | gron --ungron
```

`-G` (grona) indexes results as `json[N]`, so
[gron](https://github.com/tomnomnom/gron)'s `--ungron` rebuilds a JSON array,
`-g` roots every result at `json` and is for grepping only.

## Miller

[`mlr`](https://github.com/johnkerl/miller) is like awk, sed, cut, join, and
sort for data formats such as CSV, TSV, JSON, JSON Lines, and
positionally-indexed.

### JSONL → CSV

```sh { title='Convert JSONL to CSV with Miller' }
iq --src books '.[]' --jsonl | mlr --ijsonl --ocsv cat
```

## Drift guard

```sh { title='Schema drift check' }
iq diff prod.orders staging.orders --schema || echo "schema drift"
```

`iq diff` follows diff(1), exit 0 when the sources match, 1 when they differ,
so it slots into a cron job or a pre-deploy check as-is.

---
icon: material/robot-outline
---

# AI agents

An AI agent that has to read or move data across a polyglot estate is the reader
`iq` is already shaped for: one binary and one language over ten backends and
their dump files, JSON in and JSON out (`--jsonl`, `--compact`, `-M`,
`--error.format json`), so nothing has to be scraped out of a native shell's
formatting. `--explain` is a dry run that never connects, so a plan can be
inspected before a single byte moves; `--dry-run` reports what a write would do
without doing it; the destructive commands are capability-gated, so one against
a backend that does not implement the port fails with a clear message instead of
emulating it; and every error is redacted, so a password in a source URI never
reaches a transcript. Queries are read-only, and a filter that would materialize
a whole keyspace is refused unless `--unbounded` is passed.

## Skill

`iq` ships an [Agent Skill](https://agentskills.io): a single Markdown file,
[`skills/iq/SKILL.md`](https://github.com/zsltg/iq/blob/main/skills/iq/SKILL.md),
that any agent reading the Agent Skills format can load. It teaches the workflow
rather than the flag list: find the source before guessing at one, `--explain`
before every scan, `--dry-run` before every write, the machine-readable output
flags and the JSON error shape, which filters need `--unbounded` and why, and
the rules around writes, so `--replace` and `iq data clear`, `iq data drop` and
`iq data delete` run only on an explicit instruction. The per-backend detail
stays in `iq --help`, `man iq` and this site, so the skill stays small enough to
load beside the task.

Install it with the cross-agent installer, which fetches once into
`~/.agents/skills` and links it into every agent's skills directory it detects:

```sh
npx skills add zsltg/iq
```

With the GitHub CLI (2.90 or newer):

```sh
gh skill install zsltg/iq
```

Without either installer, copy the raw file into wherever the agent in use looks
for skills:

```sh
mkdir -p ~/.claude/skills/iq
curl -fsSL https://raw.githubusercontent.com/zsltg/iq/main/skills/iq/SKILL.md \
  -o ~/.claude/skills/iq/SKILL.md
```

The whole manual is also served as one file,
[llms-full.txt](https://zsltg.github.io/iq/llms-full.txt), so an agent can read
every page of this site in a single fetch.

---
icon: material/robot-outline
---

# AI agents

An AI agent that has to read or move data across a polyglot estate is the
reader `iq` is already shaped for, one binary and one language over ten
backends and their dump files, JSON in and JSON out (`--jsonl`, `--compact`,
`-M`, `--error.format json`), so nothing has to be scraped out of a native
shell's formatting.

`--explain` is a dry run that never connects, so a plan can be inspected before
a single byte moves. `--dry-run` reports the effect of a write without doing
it.

The destructive commands are capability-gated, so one against a backend that
does not implement the port fails with a clear message instead of emulating it
and every error is redacted, so a password in a source URI never reaches a
transcript.

Queries are read-only, and a filter that must materialize a whole keyspace is
refused unless `--unbounded` is passed.

## Skill

`iq` ships an [Agent Skill](https://agentskills.io), a single Markdown file,
[`skills/iq/SKILL.md`](https://github.com/zsltg/iq/blob/main/skills/iq/SKILL.md),
that any agent reading the Agent Skills format can load.

It teaches the workflow rather than the flag list, find the source before
guessing at one, `--explain` before every scan, `--dry-run` before every write,
the machine-readable output flags and the JSON error shape, which filters need
`--unbounded` and why, and the rules around writes, so `--replace` and
`iq data clear`, `iq data drop` and `iq data delete` run only on an explicit
instruction.

The per-backend detail stays in `iq --help`, `man iq` and this site, so the
skill stays small enough to load beside the task.

Install it with the cross-agent installer, which fetches once into
`~/.agents/skills` and links it into every agent's skills directory it detects:

```sh
npx skills add zsltg/iq
```

With the GitHub CLI (2.90 or newer):

```sh
gh skill install zsltg/iq
```

Without either installer, copy the raw file into wherever the agent in use
looks for skills:

```sh
mkdir -p ~/.claude/skills/iq
curl -fsSL https://raw.githubusercontent.com/zsltg/iq/main/skills/iq/SKILL.md \
  -o ~/.claude/skills/iq/SKILL.md
```

## MCP server

`iq mcp` serves the same query core as a
[Model Context Protocol](https://modelcontextprotocol.io) server, speaking
JSON-RPC over stdin and stdout.

It is the CLI's operations as tools, over the same saved sources and the same
engine, so an agent that cannot run shell commands still gets the whole
command set. It targets the 2026-07-28 specification revision and negotiates back
to 2025-11-25 for an older client.

### Client configuration

The server is the binary itself, so a client only needs the command. The
inherited `--timeout` defaults to 5 seconds, which is short for a scan, so pass
a longer one. Every example below registers the same server, `iq mcp --timeout
30s`, under the name `iq`.

[Claude Code](https://claude.com/claude-code):

```sh
claude mcp add iq -- iq mcp --timeout 30s
```

[Codex CLI](https://openai.com/codex/) (the `--` separates the server command
from Codex's own options, the same entry can be written by hand as
`[mcp_servers.iq]` in `~/.codex/config.toml`):

```sh
codex mcp add iq -- iq mcp --timeout 30s
```

[Gemini CLI](https://geminicli.com) (the `--` matters here too, a `--timeout`
before it is Gemini's own connection timeout in milliseconds, not iq's):

```sh
gemini mcp add iq iq mcp -- --timeout 30s
```

[Cursor](https://cursor.com) (`.cursor/mcp.json` in the project, or
`~/.cursor/mcp.json` for every project), [Cline](https://cline.bot)
(`~/.cline/mcp.json` for the CLI, the MCP Servers panel's Configure tab in the
IDE extensions), [Antigravity](https://antigravity.google)
(`~/.gemini/config/mcp_config.json`, or `.agents/mcp_config.json` in the
workspace) and the Gemini CLI settings file (`~/.gemini/settings.json`) all
take the same `mcpServers` block:

```json
{
  "mcpServers": {
    "iq": {
      "command": "iq",
      "args": ["mcp", "--timeout", "30s"]
    }
  }
}
```

[Copilot](https://github.com/features/copilot) in VS Code (`.vscode/mcp.json`
in the workspace, or the user profile file behind the
`MCP: Open User Configuration` command) names the map `servers` and wants the
transport spelled out:

```json
{
  "servers": {
    "iq": {
      "type": "stdio",
      "command": "iq",
      "args": ["mcp", "--timeout", "30s"]
    }
  }
}
```

[OpenCode](https://opencode.ai) (`opencode.json`) names it `mcp`, calls a stdio
server `local` and takes the command as one array:

```json
{
  "mcp": {
    "iq": {
      "type": "local",
      "command": ["iq", "mcp", "--timeout", "30s"],
      "enabled": true
    }
  }
}
```

[pi.dev](https://pi.dev) ships no MCP client by design, it expects a CLI plus
a skill, which is exactly what `iq` and the [Skill](#skill) above are, install
the skill and pi drives the binary directly.

Any other client that takes a stdio server block needs the same two facts, the
command `iq` and the arguments `mcp --timeout 30s`.

The server inherits the saved sources and the keyring of whoever starts it, so
point an agent at a config holding only the sources it is allowed to use rather than
your own:

```sh
iq mcp --config ~/.config/iq/agent.toml --timeout 30s
```

Register that config's sources with the same `iq add --config
~/.config/iq/agent.toml ...` you use anywhere else.

### Safety model

- **Read-only by default.** The write, exec and lifecycle tools exist only
  behind `--allow`, and a tool that is not allowed is never registered, it is
  absent from `tools/list` and unknown to the server, so a client cannot call it
  by name. `--allow writes` adds `iq_insert`, `--allow exec` adds `iq_exec` and
  `--allow destructive` adds `iq_data_clear`, `iq_data_drop` and
  `iq_data_delete` (and permits `iq_insert`'s `replace`). The flag is
  repeatable.
- **Every result is bounded.** `--max-items` (200) and `--max-bytes` (256 KiB,
  roughly 64k tokens) are hard caps. A per-call `max_items` or `max_bytes` can
  only lower them, never raise them. A capped result comes back with
  `truncated: true` rather than an error, so the agent knows there was more.
- **Every call is bounded.** The inherited `--timeout` bounds each call, and a
  per-call `timeout` can only shorten it.
- **Confirmations.** A destructive call without `confirm: true` does not
  proceed. Where the client can ask its user, the server returns an
  input-required result carrying the question and the client retries the call
  with the answer. Where it cannot, the call comes back refused, naming what to
  pass. The CLI's `--force` has no counterpart here, a confirmed call is the
  confirmation.
- **`iq_explain` first.** It never connects, and it names the route and the
  pushed-down conjuncts, so a plan can be read before a scan runs.
- **Errors are redacted.** Every failure is a tool result carrying the CLI's
  `{"error":{"message","causes"}}` document with every connection URI redacted,
  so no raw driver error and no stored password reaches a transcript.

### Tools

`readOnly` marks a tool that never modifies anything. `destructive` marks one
that can. Every tool declares `openWorldHint: false`, the sources are a closed,
configured set. The annotations are display hints, not the gate, `--allow` is.

| Tool | Allowed by | Annotations | What it does |
| --- | --- | --- | --- |
| `iq_sources` | always | readOnly, idempotent | The handles this server can access, with each URI's password redacted |
| `iq_ping` | always | readOnly, idempotent | Round-trip one cheap backend command and report the time |
| `iq_explain` | always | readOnly, idempotent | The access plan for a filter, without connecting |
| `iq_query` | always | readOnly, idempotent | Run a jq filter. Returns `{items, count, truncated}` |
| `iq_inspect` | always | readOnly, idempotent | A backend's native introspection, optionally narrowed by `only` |
| `iq_schema` | always | readOnly, idempotent | A draft 2020-12 JSON Schema inferred from a sample |
| `iq_diff` | always | readOnly, idempotent | Compare two sources by data, stats, or inferred schema |
| `iq_insert` | `--allow writes` | destructive, idempotent | Copy items into another source. `no_overwrite` defaults to true |
| `iq_exec` | `--allow exec` | destructive | Forward a command to the backend verbatim |
| `iq_data_clear` | `--allow destructive` | destructive, idempotent | Empty a container, keeping it |
| `iq_data_drop` | `--allow destructive` | destructive, idempotent | Remove a container entirely |
| `iq_data_delete` | `--allow destructive` | destructive, idempotent | Remove named keys, keeping the container |

Every tool returns `structuredContent` against a declared `outputSchema`, plus
the same JSON in a text block for a client that reads only unstructured content.
`tools/list` is sorted by name and cacheable for an hour with a private scope,
because only a restart can change it.

### Limits

- stdio only. There is no HTTP transport, so the server is a child process of
  its client and reachable by nothing else.
- The tool set is fixed at process start. Changing `--allow` means restarting
  the server.
- The server advertises the `tools` capability alone. Prompts, resources,
  sampling, roots and protocol-level logging are not implemented, the last three
  are deprecated as of the 2026-07-28 revision. Diagnostics go to stderr, or to
  the `--log` file, never to stdout, which carries the protocol and nothing
  else.
- A long dump is not a good tool result. Cap it, or run the CLI and read the
  file.

The whole manual is also served as one file,
[llms-full.txt](https://zsltg.github.io/iq/llms-full.txt), so an agent can read
every page of this site in a single fetch.

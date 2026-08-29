# mcp-cli

[![Test](https://github.com/tkukushkin/mcp-cli/actions/workflows/test.yml/badge.svg)](https://github.com/tkukushkin/mcp-cli/actions/workflows/test.yml)
[![codecov](https://codecov.io/gh/tkukushkin/mcp-cli/graph/badge.svg)](https://codecov.io/gh/tkukushkin/mcp-cli)

A single-binary MCP client for the shell. It calls one tool on one MCP server and
prints the bare payload — no envelope, no session juggling, no hand-built JSON-RPC.

Servers are not configured twice: `mcp-cli` reads the same configuration Claude Code and Codex
use, so anything in `claude mcp list` or Codex's own config is callable from a script.

```console
echo '{"libraryName": "Go", "query": "http server"}' | mcp-cli context7 resolve-library-id
```

## Install

```console
go install github.com/tkukushkin/mcp-cli@latest
```

Or grab a prebuilt binary for `darwin`, `linux` or `windows` on `amd64`/`arm64`
from the [releases page](https://github.com/tkukushkin/mcp-cli/releases).

To teach a harness about the binary, install the skill it carries:

```console
mcp-cli install-skill
```

It writes `skills/mcp-cli/` into the configuration directory of every harness installed here
(`~/.claude`, `~/.codex`), so the instructions each one reads always match the installed
version — rerun it after an upgrade. Pass `--harness claude|codex`, or set
`MCP_CLI_DEFAULT_HARNESS`, to install for just one. Each directory ignores itself in git.

## Usage

```console
mcp-cli <server> <tool>
```

- `<server>` — the name as it appears in `claude mcp list`, or in Codex's `mcp_servers` config.
- `<tool>` — the bare tool name, without the `mcp__<server>__` prefix.
- **Arguments** are a JSON object on stdin. For a tool that takes none, use
  `< /dev/null` — or just run it interactively, since a TTY stdin also means `{}`.
- **stdout** is the tool's payload: `structuredContent` as compact JSON when the server provides it,
  otherwise the text content as-is (which may itself be JSON).
- **Exit code** is 0 on success, 1 on failure, with the error text on stderr
  (server not found, connection failure, or the tool's own error message).

## Configuration

No config file of its own. Which harness's configuration it reads is decided once, in order:

1. `--harness claude` or `--harness codex`, if given.
2. Otherwise, the environment marker of whichever harness launched `mcp-cli`: `CLAUDECODE` for
   Claude Code, `CODEX_THREAD_ID` for Codex. Both set at once (a harness launched from inside
   another) can't tell who is asking, so it counts as neither.
3. Otherwise, `MCP_CLI_DEFAULT_HARNESS`.
4. Otherwise both harnesses are searched: the single owner wins if exactly one of them
   configures the name, and it is an error if both do.

A harness selected by any of the first three is strict: a miss there is an error, not a
fallback to the other.

Within Claude Code's configuration, servers are looked up by name, first match wins, in the
working directory and then in each directory above it:

1. `~/.claude.json` — `projects["<dir>"].mcpServers` (local scope)
2. `<dir>/.mcp.json` — `mcpServers` (project scope)
3. `~/.claude.json` — `mcpServers` (user scope)

`${VAR}` and `${VAR:-default}` in a config value are expanded from the environment, as Claude
Code expands them — Codex does not expand its own configuration, so this applies to Claude
Code's configuration only.

Within Codex's configuration, project-level `.codex/config.toml` layers from the working
directory upward, closest wins, but only for a directory that is marked trusted in
`$CODEX_HOME/config.toml` (`[projects."<path>"]` with `trust_level = "trusted"`) — an untrusted
project's config is ignored, exactly as Codex ignores it. The trust entry may sit on the
directory itself or on an ancestor up to its repository root; the search stops at the first
directory holding a `.git` entry, so a repository cloned into a trusted directory does not
inherit that trust. After that, `$CODEX_HOME/config.toml`'s
own `mcp_servers` table is consulted. `$CODEX_HOME` defaults to `~/.codex`.

## OAuth

Servers behind `claude mcp login` or `codex mcp login` work: `mcp-cli` shares OAuth credentials
with whichever harness configured the server, refreshing them in place when they expire and
writing the refresh back into that harness's own store — for Codex, whichever of its three
credential stores (OS keyring, encrypted secrets file, plaintext fallback) held the session.

## Scope

`tools/call` only, deliberately. Discovery (`tools/list`, resources, prompts) is what
an MCP host is for; this is the escape hatch for when you're not in one.

## License

MIT

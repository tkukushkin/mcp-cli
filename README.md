# mcp-cli

[![Test](https://github.com/tkukushkin/mcp-cli/actions/workflows/test.yml/badge.svg)](https://github.com/tkukushkin/mcp-cli/actions/workflows/test.yml)
[![codecov](https://codecov.io/gh/tkukushkin/mcp-cli/graph/badge.svg)](https://codecov.io/gh/tkukushkin/mcp-cli)

A single-binary MCP client for the shell. It calls one tool on one MCP server and
prints the bare payload — no envelope, no session juggling, no hand-built JSON-RPC.

Servers are not configured twice: `mcp-cli` reads the same configuration Claude Code
uses, so anything in `claude mcp list` is callable from a script.

```console
echo '{"libraryName": "Go", "query": "http server"}' | mcp-cli context7 resolve-library-id
```

## Install

```console
go install github.com/tkukushkin/mcp-cli@latest
```

Or grab a prebuilt binary for `darwin`, `linux` or `windows` on `amd64`/`arm64`
from the [releases page](https://github.com/tkukushkin/mcp-cli/releases).

To teach Claude Code about the binary, install the skill it carries:

```console
mcp-cli install-skill
```

It writes `~/.claude/skills/mcp-cli/`, so the instructions Claude reads always match the
installed version — rerun it after an upgrade. The directory ignores itself in git.

## Usage

```console
mcp-cli <server> <tool>
```

- `<server>` — the name as it appears in `claude mcp list`.
- `<tool>` — the bare tool name, without the `mcp__<server>__` prefix.
- **Arguments** are a JSON object on stdin. For a tool that takes none, use
  `< /dev/null` — or just run it interactively, since a TTY stdin also means `{}`.
- **stdout** is the tool's payload: `structuredContent` as compact JSON when the server provides it,
  otherwise the text content as-is (which may itself be JSON).
- **Exit code** is 0 on success, 1 on failure, with the error text on stderr
  (server not found, connection failure, or the tool's own error message).

## Configuration

No config file of its own. Servers are looked up by name, first match wins, in the
working directory and then in each directory above it:

1. `~/.claude.json` — `projects["<dir>"].mcpServers` (local scope)
2. `<dir>/.mcp.json` — `mcpServers` (project scope)
3. `~/.claude.json` — `mcpServers` (user scope)

`${VAR}` and `${VAR:-default}` in a config value are expanded from the environment,
as Claude Code expands them.

## OAuth

Servers behind `claude mcp login` work: `mcp-cli` shares OAuth credentials with Claude Code,
refreshing them in place when they expire.

## Scope

`tools/call` only, deliberately. Discovery (`tools/list`, resources, prompts) is what
an MCP host is for; this is the escape hatch for when you're not in one.

## License

MIT

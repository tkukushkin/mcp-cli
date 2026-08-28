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

No config file of its own. Servers are looked up by name, first match wins:

1. `./.mcp.json` — `mcpServers` (project scope)
2. `~/.claude.json` — `projects["$PWD"].mcpServers` (local scope)
3. `~/.claude.json` — `mcpServers` (user scope)

## OAuth

Servers authenticated through `claude mcp login` work: `mcp-cli` reads the access token
Claude Code already holds for that server and sends it as a bearer token. Nothing to log
in to twice.

The token is read from wherever Claude Code keeps its credentials — the Keychain on
macOS, `~/.claude/.credentials.json` on Linux and Windows, or `$CLAUDE_CONFIG_DIR`
when that is set. `CLAUDE_CONFIG_DIR` takes precedence everywhere, including macOS.

Two deliberate limits:

- **Only the access token is used, never the refresh token.** Refresh tokens rotate:
  refreshing here would invalidate the copy Claude Code holds and log the harness out
  of that server. When the stored token has expired, `mcp-cli` says so and stops rather
  than refreshing it — call the server once from Claude Code and it refreshes itself.
- **Nothing is ever written back.** No lock, no race with Claude Code over the same
  credential store.

An `Authorization` header in the server's own config always wins, so an entry with a
static token keeps working untouched. If no stored session exists, the call goes out
unauthenticated and the server decides.

The credential format is not a documented interface, so a Claude Code update can change
it. When that happens `mcp-cli` falls back to calling unauthenticated rather than failing.

## Scope

`tools/call` only, deliberately. Discovery (`tools/list`, resources, prompts) is what
an MCP host is for; this is the escape hatch for when you're not in one.

## License

MIT

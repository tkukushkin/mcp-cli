---
name: mcp-cli
description: Use when an MCP tool must be called from a plain shell or script, or when MCP params/results should flow through files to keep AI context small — reusing servers already configured for Claude Code or Codex.
---

# mcp-cli

Calls one tool on one MCP server from the shell, resolving the server in a configuration already
in place for Claude Code or Codex.

```bash
result=$(mktemp)    # or a file in your scratchpad directory
echo '{"key": "value"}' | mcp-cli <server> <tool> > "$result"
jq -r '<selector>' "$result"    # feed AI context only a projection, not the whole result
```

- `<server>` is the name as it appears in `claude mcp list` or in Codex's `config.toml`; `<tool>`
  is the bare tool name (no `mcp__` prefix).
- Arguments are a JSON object on stdin; `< /dev/null` for a tool that takes none.
- stdout is the tool's bare payload, not the MCP envelope, and may itself be JSON.
- Exit 1 reports the failure on stderr. The server's own stderr is discarded unless `-v`.
- `--harness claude|codex` picks whose configuration to read when both define the name.
- `tools/call` only. There is no way to list tools or check a schema, so verify the parameters
  before calling — a wrong guess surfaces only as the server's validation error.

package main

import (
	"errors"
	"fmt"
)

type harnessKind string

const (
	harnessUnknown harnessKind = ""
	harnessClaude  harnessKind = "claude"
	harnessCodex   harnessKind = "codex"
)

func parseHarness(value string) (harnessKind, error) {
	switch harnessKind(value) {
	case harnessClaude, harnessCodex:
		return harnessKind(value), nil
	}
	return harnessUnknown, fmt.Errorf("unknown harness %q: use %q or %q", value, harnessClaude, harnessCodex)
}

// selectHarness decides whose configuration to search: the explicit flag, then the marker
// the running harness leaves in the environment, then the user's declared default. Both
// markers at once means a nested session (Claude ran codex, codex ran mcp-cli) where the
// inherited environment cannot tell who is asking, so it counts as no marker.
func selectHarness(flagValue string, getenv func(string) string) (harnessKind, error) {
	if flagValue != "" {
		return parseHarness(flagValue)
	}
	claude := getenv("CLAUDECODE") != ""
	codex := getenv("CODEX_THREAD_ID") != ""
	switch {
	case claude && !codex:
		return harnessClaude, nil
	case codex && !claude:
		return harnessCodex, nil
	}
	if value := getenv("MCP_CLI_DEFAULT_HARNESS"); value != "" {
		return parseHarness(value)
	}
	return harnessUnknown, nil
}

// findServer routes the lookup. A determined harness is strict — a miss is an error rather
// than a fallback, so the answer never depends on what another harness happens to have.
// Undetermined searches both and refuses a name both define, even identically: guessing
// here would pick an OAuth session too.
func findServer(name, cwd, home string, harness harnessKind) (*serverConfig, error) {
	switch harness {
	case harnessClaude:
		return findServerConfig(name, cwd, home)
	case harnessCodex:
		return findCodexServerConfig(name, cwd, codexHomeDir(home))
	}
	claude, claudeErr := findServerConfig(name, cwd, home)
	if claudeErr != nil && !errors.Is(claudeErr, errServerNotFound) {
		return nil, claudeErr
	}
	codex, codexErr := findCodexServerConfig(name, cwd, codexHomeDir(home))
	if codexErr != nil && !errors.Is(codexErr, errServerNotFound) {
		return nil, codexErr
	}
	switch {
	case claude != nil && codex != nil:
		return nil, fmt.Errorf("MCP server %q is configured for both Claude Code and Codex: pass --harness or set MCP_CLI_DEFAULT_HARNESS", name)
	case claude != nil:
		return claude, nil
	case codex != nil:
		return codex, nil
	}
	return nil, fmt.Errorf("%w: %q is neither in Claude Code's configuration (.mcp.json, ~/.claude.json) nor in Codex's (config.toml)", errServerNotFound, name)
}

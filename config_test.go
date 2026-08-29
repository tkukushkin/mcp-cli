package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setHome points os.UserHomeDir at dir. It reads USERPROFILE on Windows, so setting HOME
// alone would leave the tests reading the real ~/.claude.json there. It also neutralizes every
// environment input selectHarness/findServer read from the real process — the harness markers,
// the declared default, and $CODEX_HOME — so a test that runs the command end to end resolves
// the same way regardless of which harness happens to be running the test suite itself.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("CLAUDECODE", "")
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("MCP_CLI_DEFAULT_HARNESS", "")
	t.Setenv("CODEX_HOME", filepath.Join(dir, ".codex"))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFindServerConfigPrefersProjectScope(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(cwd, ".mcp.json"), `{"mcpServers": {"srv": {"command": "project"}}}`)
	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"srv": {"command": "user"}}}`)

	cfg, err := findServerConfig("srv", cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Command != "project" {
		t.Errorf("got %q, want %q", cfg.Command, "project")
	}
}

func TestFindServerConfigPrefersLocalScopeOverUserScope(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"),
		`{"projects": {"`+cwd+`": {"mcpServers": {"srv": {"command": "local"}}}},
		  "mcpServers": {"srv": {"command": "user"}}}`)

	cfg, err := findServerConfig("srv", cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Command != "local" {
		t.Errorf("got %q, want %q", cfg.Command, "local")
	}
}

// `claude mcp add -s local` is how a checked-in .mcp.json entry is overridden, so the local
// scope has to win over the project one here as it does in Claude Code.
func TestFindServerConfigPrefersLocalScopeOverProjectScope(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(cwd, ".mcp.json"), `{"mcpServers": {"srv": {"command": "project"}}}`)
	writeFile(t, filepath.Join(home, ".claude.json"),
		`{"projects": {"`+cwd+`": {"mcpServers": {"srv": {"command": "local"}}}}}`)

	cfg, err := findServerConfig("srv", cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Command != "local" {
		t.Errorf("got %q, want %q", cfg.Command, "local")
	}
}

// The project a server is configured for does not stop at its root directory: mcp-cli is run
// from wherever the script happens to sit.
func TestFindServerConfigSearchesParentDirectories(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, ".mcp.json"), `{"mcpServers": {"srv": {"command": "project"}}}`)
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}

	cfg, err := findServerConfig("srv", nested, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Command != "project" {
		t.Errorf("got %q, want %q", cfg.Command, "project")
	}
}

func TestFindServerConfigExpandsEnvironmentVariables(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	t.Setenv("MCP_CLI_TEST_TOKEN_VALUE", "s3cret")
	writeFile(t, filepath.Join(cwd, ".mcp.json"), `{"mcpServers": {"srv": {
		"url": "https://example.test/mcp",
		"headers": {"Authorization": "Bearer ${MCP_CLI_TEST_TOKEN_VALUE}"},
		"args": ["--host=${MCP_CLI_TEST_UNSET_HOST:-localhost}"]
	}}}`)

	cfg, err := findServerConfig("srv", cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Headers["Authorization"] != "Bearer s3cret" {
		t.Errorf("got %q", cfg.Headers["Authorization"])
	}
	if cfg.Args[0] != "--host=localhost" {
		t.Errorf("got %q, want the default of an unset variable", cfg.Args[0])
	}
}

// An empty token or URL fails far from its cause, so an unset variable is reported instead.
func TestFindServerConfigReportsAnUnsetVariable(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(cwd, ".mcp.json"),
		`{"mcpServers": {"srv": {"url": "https://${MCP_CLI_TEST_UNSET_HOST}/mcp"}}}`)

	_, err := findServerConfig("srv", cwd, home)
	if err == nil || !strings.Contains(err.Error(), "MCP_CLI_TEST_UNSET_HOST") {
		t.Fatalf("got %v, want the unset variable named", err)
	}
}

func TestFindServerConfigFallsBackToUserScope(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"),
		`{"projects": {"/elsewhere": {"mcpServers": {"srv": {"command": "other"}}}},
		  "mcpServers": {"srv": {"url": "https://example.test/mcp", "headers": {"X-Key": "v"}}}}`)

	cfg, err := findServerConfig("srv", cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://example.test/mcp" || cfg.Headers["X-Key"] != "v" {
		t.Errorf("got %+v", cfg)
	}
}

func TestFindServerConfigMissingServer(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"other": {"command": "x"}}}`)

	if _, err := findServerConfig("srv", cwd, home); err == nil {
		t.Fatal("expected an error for a missing server")
	}
}

func TestFindServerConfigMissingFilesAreSkipped(t *testing.T) {
	if _, err := findServerConfig("srv", t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("expected an error for a missing server")
	}
}

func TestFindServerConfigMalformedJSON(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(cwd, ".mcp.json"), `{not json`)

	if _, err := findServerConfig("srv", cwd, home); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestFindServerConfigMalformedClaudeJSON(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": `)

	if _, err := findServerConfig("srv", cwd, home); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestReadConfigReportsUnreadablePath(t *testing.T) {
	if _, err := readConfig(t.TempDir()); err == nil {
		t.Fatal("expected an error when the config path is a directory")
	}
}

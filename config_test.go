package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
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

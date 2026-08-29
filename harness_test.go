package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestSelectHarness(t *testing.T) {
	cases := []struct {
		name string
		flag string
		vars map[string]string
		want harnessKind
	}{
		{"flag beats markers", "codex", map[string]string{"CLAUDECODE": "1"}, harnessCodex},
		{"claude marker", "", map[string]string{"CLAUDECODE": "1"}, harnessClaude},
		{"codex marker", "", map[string]string{"CODEX_THREAD_ID": "t1"}, harnessCodex},
		{"both markers cancel out", "", map[string]string{"CLAUDECODE": "1", "CODEX_THREAD_ID": "t1"}, harnessUnknown},
		{"default env", "", map[string]string{"MCP_CLI_DEFAULT_HARNESS": "codex"}, harnessCodex},
		{"marker beats default env", "", map[string]string{"CLAUDECODE": "1", "MCP_CLI_DEFAULT_HARNESS": "codex"}, harnessClaude},
		{"nothing set", "", nil, harnessUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := selectHarness(c.flag, env(c.vars))
			if err != nil || got != c.want {
				t.Fatalf("selectHarness(%q, %v) = %q, %v; want %q", c.flag, c.vars, got, err, c.want)
			}
		})
	}
}

func TestSelectHarnessRejectsUnknownValues(t *testing.T) {
	if _, err := selectHarness("cursor", env(nil)); err == nil {
		t.Fatal("an unknown --harness value must be an error")
	}
	if _, err := selectHarness("", env(map[string]string{"MCP_CLI_DEFAULT_HARNESS": "cursor"})); err == nil {
		t.Fatal("an unknown MCP_CLI_DEFAULT_HARNESS must be an error")
	}
}

func TestFindServerAmbiguous(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"s": {"command": "claude-cmd"}}}`)
	writeFile(t, filepath.Join(codexHome, "config.toml"), "[mcp_servers.s]\ncommand = \"codex-cmd\"\n")

	_, err := findServer("s", cwd, home, harnessUnknown)
	if err == nil || !strings.Contains(err.Error(), "--harness") {
		t.Fatalf("want an ambiguity error naming --harness, got %v", err)
	}

	cfg, err := findServer("s", cwd, home, harnessCodex)
	if err != nil || cfg.Command != "codex-cmd" {
		t.Fatalf("explicit harness must resolve, got %v, %v", cfg, err)
	}
}

func TestFindServerFallsBackAcrossHarnesses(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	writeFile(t, filepath.Join(codexHome, "config.toml"), "[mcp_servers.only]\ncommand = \"codex-cmd\"\n")

	cfg, err := findServer("only", cwd, home, harnessUnknown)
	if err != nil || cfg.Command != "codex-cmd" {
		t.Fatalf("undetermined harness must find the single owner, got %v, %v", cfg, err)
	}

	if _, err := findServer("only", cwd, home, harnessClaude); !errors.Is(err, errServerNotFound) {
		t.Fatalf("strict harness must not fall back, got %v", err)
	}
}

// A miss under a determined harness has to point at the other one, or a user inside Claude Code
// calling a Codex-configured server never learns that --harness exists.
func TestStrictMissNamesTheOtherHarness(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	_, err := findServer("absent", cwd, home, harnessClaude)
	if err == nil || !strings.Contains(err.Error(), "--harness codex") {
		t.Fatalf("Claude miss must mention --harness codex, got %v", err)
	}
	_, err = findServer("absent", cwd, home, harnessCodex)
	if err == nil || !strings.Contains(err.Error(), "--harness claude") {
		t.Fatalf("Codex miss must mention --harness claude, got %v", err)
	}
}

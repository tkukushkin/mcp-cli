package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runInstallSkillCmd runs install-skill directly rather than through runCommand: install-skill
// resolves its harness only from the flag and MCP_CLI_DEFAULT_HARNESS, and runCommand's setHome
// unconditionally resets both CODEX_HOME and MCP_CLI_DEFAULT_HARNESS, which would clobber the
// exact env these tests need to control.
func runInstallSkillCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := callToolCmd()
	cmd.SetArgs(append([]string{"install-skill"}, args...))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestInstallSkillWritesTheEmbeddedDocument(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)

	dirs, err := installSkill(harnessClaude)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(config, "skills", "mcp-cli")
	if len(dirs) != 1 || dirs[0] != want {
		t.Fatalf("got %v, want [%q]", dirs, want)
	}
	doc, err := os.ReadFile(filepath.Join(want, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(doc) != skillDoc || !strings.HasPrefix(string(doc), "---\nname: mcp-cli\n") {
		t.Errorf("got %q", doc)
	}
	ignore, err := os.ReadFile(filepath.Join(want, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ignore) != "*\n" {
		t.Errorf("got %q, want the whole directory ignored", ignore)
	}
}

func TestInstallSkillFallsBackToTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	dirs, err := installSkill(harnessClaude)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(home, ".claude", "skills", "mcp-cli")
	if len(dirs) != 1 || dirs[0] != want {
		t.Errorf("got %v", dirs)
	}
}

func TestInstallSkillOverwritesAnOlderVersion(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	dir := filepath.Join(config, "skills", "mcp-cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "SKILL.md"), "stale")

	if _, err := installSkill(harnessClaude); err != nil {
		t.Fatal(err)
	}

	doc, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(doc) != skillDoc {
		t.Errorf("got %q, want the embedded document", doc)
	}
}

func TestInstallSkillCommandReportsTheDirectory(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	t.Setenv("MCP_CLI_DEFAULT_HARNESS", "")

	out, err := runInstallSkillCmd(t)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, filepath.Join(config, "skills", "mcp-cli")) {
		t.Errorf("got %q", out)
	}
}

func TestInstallSkillCommandReportsBothHarnesses(t *testing.T) {
	claudeDir := t.TempDir()
	codexHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("MCP_CLI_DEFAULT_HARNESS", "")

	out, err := runInstallSkillCmd(t)
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{
		filepath.Join(claudeDir, "skills", "mcp-cli"),
		filepath.Join(codexHome, "skills", "mcp-cli"),
	} {
		if !strings.Contains(out, dir) {
			t.Errorf("got %q, want to contain %q", out, dir)
		}
	}
}

func TestInstallSkillCommandHonorsDefaultHarnessEnv(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), "fresh")
	claudeDir := filepath.Join(t.TempDir(), "unused")
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("MCP_CLI_DEFAULT_HARNESS", "codex")

	out, err := runInstallSkillCmd(t)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(codexHome, "skills", "mcp-cli")
	if !strings.Contains(out, want) {
		t.Errorf("got %q, want to contain %q", out, want)
	}
	if strings.Contains(out, filepath.Join(claudeDir, "skills", "mcp-cli")) {
		t.Errorf("got %q, want only Codex installed", out)
	}
}

func TestInstallSkillCommandReportsPartialFailure(t *testing.T) {
	claudeDir := t.TempDir()
	// A regular file standing where Codex's root should be a directory forces its MkdirAll
	// to fail, after Claude's install already succeeded and was recorded.
	codexHome := filepath.Join(t.TempDir(), "codex-blocked")
	if err := os.WriteFile(codexHome, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("MCP_CLI_DEFAULT_HARNESS", "")

	out, err := runInstallSkillCmd(t)
	if err == nil {
		t.Fatal("want an error when Codex's write fails")
	}
	if !strings.Contains(out, filepath.Join(claudeDir, "skills", "mcp-cli")) {
		t.Errorf("got %q, want the successful Claude directory reported despite the failure", out)
	}
}

func TestInstallSkillAllInstalledHarnesses(t *testing.T) {
	claudeDir := t.TempDir()
	codexHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CODEX_HOME", codexHome)

	dirs, err := installSkill(harnessUnknown)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 2 {
		t.Fatalf("want both harnesses installed, got %v", dirs)
	}
	for _, dir := range []string{
		filepath.Join(claudeDir, "skills", "mcp-cli", "SKILL.md"),
		filepath.Join(codexHome, "skills", "mcp-cli", "SKILL.md"),
	} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInstallSkillSkipsAbsentHarness(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "nonexistent"))

	dirs, err := installSkill(harnessUnknown)
	if err != nil || len(dirs) != 1 {
		t.Fatalf("want only the installed harness, got %v, %v", dirs, err)
	}
}

func TestInstallSkillExplicitHarnessCreates(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), "fresh")
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "unused"))

	dirs, err := installSkill(harnessCodex)
	if err != nil || len(dirs) != 1 || !strings.HasPrefix(dirs[0], codexHome) {
		t.Fatalf("an explicit harness installs even into a fresh directory: %v, %v", dirs, err)
	}
}

func TestInstallSkillNoHarnessInstalled(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "none"))
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "none"))

	if _, err := installSkill(harnessUnknown); err == nil {
		t.Fatal("no installed harness must be an error, not silent directory creation")
	}
}

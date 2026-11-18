package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallSkillWritesTheEmbeddedDocument(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)

	dir, err := installSkill()
	if err != nil {
		t.Fatal(err)
	}

	if dir != filepath.Join(config, "skills", "mcp-cli") {
		t.Errorf("got %q", dir)
	}
	doc, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(doc) != skillDoc || !strings.HasPrefix(string(doc), "---\nname: mcp-cli\n") {
		t.Errorf("got %q", doc)
	}
	ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
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

	dir, err := installSkill()
	if err != nil {
		t.Fatal(err)
	}

	if dir != filepath.Join(home, ".claude", "skills", "mcp-cli") {
		t.Errorf("got %q", dir)
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

	if _, err := installSkill(); err != nil {
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

	out, err := runCommand(t, nil, "", "install-skill")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, filepath.Join(config, "skills", "mcp-cli")) {
		t.Errorf("got %q", out)
	}
}

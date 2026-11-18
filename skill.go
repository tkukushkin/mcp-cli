package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

//go:embed skill/SKILL.md
var skillDoc string

func skillDir() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "skills", "mcp-cli"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "skills", "mcp-cli"), nil
}

// installSkill writes the skill Claude Code reads to learn about this binary. It is embedded rather
// than fetched so that the instructions always describe the version that is installed.
//
// The .gitignore covers a configuration directory that is itself a git repository: the skill belongs
// to whichever mcp-cli is on PATH, not to the user's dotfiles.
func installSkill() (string, error) {
	dir, err := skillDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n"), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillDoc), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

func installSkillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install-skill",
		Short: "Install the mcp-cli skill for Claude Code into ~/.claude/skills.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			dir, err := installSkill()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "installed the mcp-cli skill in %s\n", dir)
			return nil
		},
	}
}

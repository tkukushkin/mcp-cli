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

// claudeConfigDir is Claude Code's configuration root: $CLAUDE_CONFIG_DIR, else <home>/.claude.
func claudeConfigDir(home string) string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(home, ".claude")
}

// writeSkill installs the skill a harness reads to learn about this binary, into dir. It is
// embedded rather than fetched so that the instructions always describe the version that is
// installed.
//
// The .gitignore covers a configuration directory that is itself a git repository: the skill belongs
// to whichever mcp-cli is on PATH, not to the user's dotfiles.
func writeSkill(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillDoc), 0o644)
}

// installSkill writes the skill for the selected harness, or — when none is selected — for
// every harness that is installed here, judged by its config directory existing. An explicit
// harness always installs, even into a fresh directory: the user named it, so absence is a
// first-run, not a mistake.
func installSkill(harness harnessKind) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	roots := map[harnessKind]string{
		harnessClaude: claudeConfigDir(home),
		harnessCodex:  codexHomeDir(home),
	}
	if harness != harnessUnknown {
		dir := filepath.Join(roots[harness], "skills", "mcp-cli")
		return []string{dir}, writeSkill(dir)
	}
	var written []string
	for _, kind := range []harnessKind{harnessClaude, harnessCodex} {
		if _, err := os.Stat(roots[kind]); err != nil {
			continue
		}
		dir := filepath.Join(roots[kind], "skills", "mcp-cli")
		if err := writeSkill(dir); err != nil {
			return written, err
		}
		written = append(written, dir)
	}
	if len(written) == 0 {
		return nil, fmt.Errorf("neither Claude Code (%s) nor Codex (%s) is installed here: pass --harness to install anyway",
			roots[harnessClaude], roots[harnessCodex])
	}
	return written, nil
}

func installSkillCmd() *cobra.Command {
	var harnessFlag string
	cmd := &cobra.Command{
		Use:   "install-skill",
		Short: "Install the mcp-cli skill for every harness installed here (Claude Code, Codex).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			// Environment markers deliberately do not narrow this: installing is a human
			// action, and a run from inside one harness should still cover both.
			value := harnessFlag
			if value == "" {
				value = os.Getenv("MCP_CLI_DEFAULT_HARNESS")
			}
			harness := harnessUnknown
			if value != "" {
				var err error
				harness, err = parseHarness(value)
				if err != nil {
					return err
				}
			}
			// Report what actually happened before returning the error: installSkill
			// hands back the directories it already wrote alongside a partial failure,
			// and a directory that got a fresh skill is news even when its sibling failed.
			dirs, err := installSkill(harness)
			for _, dir := range dirs {
				fmt.Fprintf(cmd.ErrOrStderr(), "installed the mcp-cli skill in %s\n", dir)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&harnessFlag, "harness", "", "Which harness to install for: claude or codex. Default: every one installed here.")
	return cmd
}

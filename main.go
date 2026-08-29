package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/spf13/cobra"
)

var version = "dev"

func getVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func readArguments(stdin *os.File) (map[string]any, error) {
	info, err := stdin.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return map[string]any{}, nil
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	var arguments map[string]any
	if err := json.Unmarshal(data, &arguments); err != nil {
		return nil, fmt.Errorf("invalid JSON arguments on stdin: %w", err)
	}
	return arguments, nil
}

func callToolCmd() *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:     "mcp-cli <server> <tool>",
		Short:   "Call an MCP tool of a server configured for Claude Code.",
		Args:    cobra.ExactArgs(2),
		Version: getVersion(),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			cfg, err := findServerConfig(args[0], cwd, home)
			if err != nil {
				return err
			}
			arguments, err := readArguments(os.Stdin)
			if err != nil {
				return err
			}

			var errlog io.Writer = io.Discard
			if verbose {
				errlog = cmd.ErrOrStderr()
			}
			result, err := callTool(cmd.Context(), cfg, args[1], arguments, errlog)
			if err != nil {
				return err
			}
			payload, err := renderPayload(result)
			if err != nil {
				return err
			}
			if result.IsError {
				return fmt.Errorf("%s", payload)
			}
			fmt.Fprintln(cmd.OutOrStdout(), payload)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Pass the MCP server stderr through instead of discarding it.")
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.CompletionOptions.DisableDefaultCmd = true
	return cmd
}

func main() {
	// Ctrl-C has to reach the context: it is what stops the MCP server this program started
	// and the `security` command that may be sitting on a Keychain prompt.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := callToolCmd().ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

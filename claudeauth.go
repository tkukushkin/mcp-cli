package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// keychainService is the macOS Keychain entry Claude Code stores its credentials in.
const keychainService = "Claude Code-credentials"

// claudeCredentials is the part of Claude Code's credential blob that holds MCP OAuth sessions.
// The map key is "<serverName>|<hash>", so entries are matched on the serverName field instead.
type claudeCredentials struct {
	MCPOAuth map[string]struct {
		ServerName  string `json:"serverName"`
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"`
	} `json:"mcpOAuth"`
}

func credentialsFile() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".credentials.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", ".credentials.json"), nil
}

func readCredentialsFile() ([]byte, error) {
	path, err := credentialsFile()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

// readClaudeCredentials returns Claude Code's credential blob, or nil when there is none.
// macOS keeps it in the Keychain; Linux and Windows keep it in a file. An explicit
// CLAUDE_CONFIG_DIR wins everywhere, which also keeps tests off the real Keychain.
func readClaudeCredentials(ctx context.Context) ([]byte, error) {
	if os.Getenv("CLAUDE_CONFIG_DIR") != "" || runtime.GOOS != "darwin" {
		return readCredentialsFile()
	}
	data, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", keychainService, "-w").Output()
	if err != nil {
		return readCredentialsFile()
	}
	return data, nil
}

// oauthToken returns the access token Claude Code holds for the named server, or an empty
// string when it holds none. An expired token is reported as an error rather than sent,
// because only Claude Code may refresh it: refresh tokens rotate, so refreshing here would
// invalidate the harness session.
func oauthToken(credentials []byte, serverName string) (string, error) {
	if len(credentials) == 0 {
		return "", nil
	}
	// The credential format is not a documented interface. If a Claude Code update changes
	// it, fall through to an unauthenticated call and let the server report the problem.
	var parsed claudeCredentials
	if err := json.Unmarshal(credentials, &parsed); err != nil {
		return "", nil
	}
	var token string
	var expiresAt int64
	for _, entry := range parsed.MCPOAuth {
		if entry.ServerName == serverName && entry.ExpiresAt >= expiresAt {
			token, expiresAt = entry.AccessToken, entry.ExpiresAt
		}
	}
	if token == "" {
		return "", nil
	}
	if expiry := time.UnixMilli(expiresAt); time.Now().After(expiry) {
		return "", fmt.Errorf(
			"the OAuth token Claude Code holds for %q expired at %s; call the server once from Claude Code to refresh it",
			serverName, expiry.Format(time.RFC3339))
	}
	return token, nil
}

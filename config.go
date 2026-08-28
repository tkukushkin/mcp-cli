package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// serverConfig is one entry of the mcpServers map in Claude Code's configuration.
type serverConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`

	// Name is the key this entry was found under; it identifies the server's OAuth session.
	Name string `json:"-"`
}

// claudeConfig covers both .mcp.json (mcpServers only) and ~/.claude.json (both fields).
type claudeConfig struct {
	MCPServers map[string]serverConfig `json:"mcpServers"`
	Projects   map[string]struct {
		MCPServers map[string]serverConfig `json:"mcpServers"`
	} `json:"projects"`
}

func readConfig(path string) (*claudeConfig, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg claudeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

// findServerConfig searches project scope, then the local and user scopes of ~/.claude.json.
func findServerConfig(name, cwd, home string) (*serverConfig, error) {
	var scopes []map[string]serverConfig

	project, err := readConfig(filepath.Join(cwd, ".mcp.json"))
	if err != nil {
		return nil, err
	}
	if project != nil {
		scopes = append(scopes, project.MCPServers)
	}

	claude, err := readConfig(filepath.Join(home, ".claude.json"))
	if err != nil {
		return nil, err
	}
	if claude != nil {
		scopes = append(scopes, claude.Projects[cwd].MCPServers, claude.MCPServers)
	}

	for _, servers := range scopes {
		if cfg, ok := servers[name]; ok {
			cfg.Name = name
			return &cfg, nil
		}
	}
	return nil, fmt.Errorf("MCP server %q not found in .mcp.json or ~/.claude.json", name)
}

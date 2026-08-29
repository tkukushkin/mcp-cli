package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var errServerNotFound = errors.New("MCP server not found")

// serverConfig is one entry of the mcpServers map in Claude Code's configuration.
type serverConfig struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`

	// Name is the key this entry was found under; it identifies the server's OAuth session.
	Name string `json:"-"`
	// Cwd is the working directory for a stdio server, set only by Codex configs.
	Cwd string `json:"-"`
	// harness records which harness's config this entry came from; it routes the OAuth lookup.
	harness harnessKind `json:"-"`
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

// findServerConfig searches the local and project scopes of the working directory and of every
// directory above it, then the user scope. Local beats project at each level, which is the
// order Claude Code resolves a name in, so an override made with `claude mcp add -s local`
// wins here too.
func findServerConfig(name, cwd, home string) (*serverConfig, error) {
	claude, err := readConfig(filepath.Join(home, ".claude.json"))
	if err != nil {
		return nil, err
	}

	for dir := cwd; ; {
		cfg, err := findInDirectory(claude, dir, name)
		if err != nil {
			return nil, err
		}
		if cfg != nil {
			return cfg, expandVariables(cfg)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	if claude != nil {
		if cfg, found := lookupServer(name, claude.MCPServers); found {
			return cfg, expandVariables(cfg)
		}
	}
	return nil, fmt.Errorf("%w: %q is not in .mcp.json or ~/.claude.json; pass --harness codex to search Codex's configuration", errServerNotFound, name)
}

// findInDirectory looks in the local scope of one directory, then in its .mcp.json.
func findInDirectory(claude *claudeConfig, dir, name string) (*serverConfig, error) {
	if cfg, found := lookupServer(name, localServers(claude, dir)); found {
		return cfg, nil
	}
	project, err := readConfig(filepath.Join(dir, ".mcp.json"))
	if err != nil || project == nil {
		return nil, err
	}
	cfg, _ := lookupServer(name, project.MCPServers)
	return cfg, nil
}

// localServers returns the local scope of one directory, which Claude Code may have keyed by
// either path when one of them is a symlink — on macOS, /tmp against /private/tmp.
func localServers(claude *claudeConfig, dir string) map[string]serverConfig {
	if claude == nil {
		return nil
	}
	if servers := claude.Projects[dir].MCPServers; servers != nil {
		return servers
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved == dir {
		return nil
	}
	return claude.Projects[resolved].MCPServers
}

func lookupServer(name string, servers map[string]serverConfig) (*serverConfig, bool) {
	cfg, ok := servers[name]
	if !ok {
		return nil, false
	}
	cfg.Name = name
	cfg.harness = harnessClaude
	return &cfg, true
}

var variablePattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// expandVariables substitutes ${VAR} and ${VAR:-default} from the environment, the way Claude
// Code does when it reads the same config. A variable that is neither set nor defaulted is an
// error rather than an empty string, which as a token or a URL would fail far from its cause.
func expandVariables(cfg *serverConfig) error {
	var missing []string
	expand := func(value string) string {
		return variablePattern.ReplaceAllStringFunc(value, func(reference string) string {
			groups := variablePattern.FindStringSubmatch(reference)
			if set, ok := os.LookupEnv(groups[1]); ok {
				return set
			}
			if groups[2] != "" {
				return groups[3]
			}
			missing = append(missing, groups[1])
			return ""
		})
	}

	cfg.Command = expand(cfg.Command)
	cfg.URL = expand(cfg.URL)
	for i, arg := range cfg.Args {
		cfg.Args[i] = expand(arg)
	}
	for name, value := range cfg.Env {
		cfg.Env[name] = expand(value)
	}
	for name, value := range cfg.Headers {
		cfg.Headers[name] = expand(value)
	}

	if len(missing) > 0 {
		return fmt.Errorf("server %q needs environment variables that are not set: %s",
			cfg.Name, strings.Join(missing, ", "))
	}
	return nil
}

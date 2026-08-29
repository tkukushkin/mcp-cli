package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// codexServerConfig is one [mcp_servers.<name>] table of Codex's config.toml. Fields Codex
// treats as runtime behavior (timeouts, tool filtering, auth mode, enabled) are ignored:
// mcp-cli is an escape hatch and the server is named explicitly.
type codexServerConfig struct {
	Command           string            `toml:"command"`
	Args              []string          `toml:"args"`
	Env               map[string]string `toml:"env"`
	EnvVars           []any             `toml:"env_vars"`
	Cwd               string            `toml:"cwd"`
	URL               string            `toml:"url"`
	BearerToken       string            `toml:"bearer_token"`
	BearerTokenEnvVar string            `toml:"bearer_token_env_var"`
	HTTPHeaders       map[string]string `toml:"http_headers"`
	EnvHTTPHeaders    map[string]string `toml:"env_http_headers"`
}

type codexProject struct {
	TrustLevel string `toml:"trust_level"`
}

// codexConfig keeps the server tables raw because the layers have to be merged before any of
// them is a complete definition; codexServerFromTable decodes the merged result.
type codexConfig struct {
	MCPServers            map[string]map[string]any `toml:"mcp_servers"`
	Projects              map[string]codexProject   `toml:"projects"`
	OAuthCredentialsStore string                    `toml:"mcp_oauth_credentials_store"`
}

// codex's OAuthCredentialsStoreMode: which stores hold MCP OAuth sessions.
const (
	codexStoreAuto    = "auto"
	codexStoreFile    = "file"
	codexStoreKeyring = "keyring"
)

// codexCredentialsStoreMode reads the policy the user set for MCP OAuth credentials. Reading
// the wrong store is worse than reading none: a session left behind in a store the user has
// moved away from refreshes with a token codex no longer tracks.
//
// A config this cannot read counts as the default. The server lookup parses the same file
// first and reports the parse error there, so nothing is swallowed by answering "auto" here;
// and a value from a newer codex is a mode this program does not implement, not a reason to
// refuse to run.
func codexCredentialsStoreMode(codexHome string) string {
	cfg, err := readCodexConfigFile(filepath.Join(codexHome, "config.toml"))
	if err != nil || cfg == nil {
		return codexStoreAuto
	}
	switch cfg.OAuthCredentialsStore {
	case codexStoreFile, codexStoreKeyring:
		return cfg.OAuthCredentialsStore
	}
	return codexStoreAuto
}

func readCodexConfigFile(path string) (*codexConfig, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg codexConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

// toServerConfig maps a Codex entry onto the shared serverConfig. Values are taken verbatim:
// Codex does no ${VAR} expansion in its config, so neither does this path. Environment-sourced
// values (env_vars, env_http_headers, bearer_token_env_var) resolve against mcp-cli's own
// environment, which is what Codex's "local" source means.
func (c codexServerConfig) toServerConfig(name string) (*serverConfig, error) {
	cfg := &serverConfig{
		Command: c.Command,
		Args:    c.Args,
		Env:     map[string]string{},
		URL:     c.URL,
		Cwd:     c.Cwd,
		Headers: map[string]string{},
		Name:    name,
		harness: harnessCodex,
	}
	for k, v := range c.Env {
		cfg.Env[k] = v
	}
	for _, v := range c.EnvVars {
		var envVarName string
		var source string
		switch t := v.(type) {
		case string:
			envVarName = t
		case map[string]any:
			envVarName, _ = t["name"].(string)
			source, _ = t["source"].(string)
		default:
			return nil, errors.New("env_vars entries must be a string or a {name, source} table")
		}
		if source == "remote" {
			continue
		}
		if value, ok := os.LookupEnv(envVarName); ok {
			cfg.Env[envVarName] = value
		}
	}
	for k, v := range c.HTTPHeaders {
		cfg.Headers[k] = v
	}
	for header, envName := range c.EnvHTTPHeaders {
		if value, ok := os.LookupEnv(envName); ok {
			cfg.Headers[header] = value
		}
	}
	switch {
	case c.BearerToken != "":
		cfg.Headers["Authorization"] = "Bearer " + c.BearerToken
	case c.BearerTokenEnvVar != "":
		value, ok := os.LookupEnv(c.BearerTokenEnvVar)
		if !ok {
			return nil, fmt.Errorf("server %q needs environment variable %s that is not set", name, c.BearerTokenEnvVar)
		}
		cfg.Headers["Authorization"] = "Bearer " + value
	}
	return cfg, nil
}

// mergeCodexTables overlays one configuration layer onto another the way codex's
// merge_toml_values does: sub-tables merge recursively, everything else the overlay names is
// replaced outright. Neither input is modified. The special cases codex's merge carries —
// feature paths, key aliases, case folding — all live under other config sections, so a plain
// recursive merge is the whole of it for an [mcp_servers.<name>] table.
func mergeCodexTables(base, overlay map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(overlay))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overlay {
		baseTable, baseIsTable := merged[key].(map[string]any)
		overlayTable, overlayIsTable := value.(map[string]any)
		if baseIsTable && overlayIsTable {
			merged[key] = mergeCodexTables(baseTable, overlayTable)
			continue
		}
		merged[key] = value
	}
	return merged
}

// codexServerFromTable re-encodes a merged table and decodes it through codexServerConfig, so
// that struct's tags stay the one place the Codex fields are mapped. One small table through
// the encoder is cheaper than a second, hand-written copy of the mapping that could drift.
func codexServerFromTable(table map[string]any, name string) (*serverConfig, error) {
	data, err := toml.Marshal(table)
	if err != nil {
		return nil, fmt.Errorf("server %q: %w", name, err)
	}
	var entry codexServerConfig
	if err := toml.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("server %q: %w", name, err)
	}
	return entry.toServerConfig(name)
}

func codexHomeDir(home string) string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(home, ".codex")
}

// findCodexServerConfig resolves a name the way Codex layers its config: the global
// $CODEX_HOME/config.toml overlaid by the project-level .codex/config.toml files from the
// working directory's ancestors down to the working directory itself, so the closest layer wins
// key by key rather than replacing the definition wholesale — a project layer that only adds an
// env variable to a globally defined server keeps that server's command, as it does in Codex.
// A project layer counts only when the global config trusts its directory — Codex refuses
// untrusted project config because it can name an arbitrary command, and so does mcp-cli.
//
// ponytail: Codex stops this layer walk at the project root (git toplevel) and reads a bare
// ${PWD}/config.toml layer too. Walking past the repo root can only reach a directory the user
// trusted explicitly, and the bare layer is undocumented, so neither is replicated; stop the
// walk at a .git entry as well if that divergence ever surprises anyone.
func findCodexServerConfig(name, cwd, codexHome string) (*serverConfig, error) {
	global, err := readCodexConfigFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		return nil, err
	}

	var layers []map[string]any // closest first
	for dir := cwd; ; {
		if filepath.Join(dir, ".codex") != codexHome && trustedByCodex(global, dir) {
			project, err := readCodexConfigFile(filepath.Join(dir, ".codex", "config.toml"))
			if err != nil {
				return nil, err
			}
			if project != nil {
				if table, ok := project.MCPServers[name]; ok {
					layers = append(layers, table)
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	merged, found := map[string]any(nil), false
	if global != nil {
		merged, found = global.MCPServers[name]
	}
	// Outermost layer first, so each nearer one overlays what is under it.
	for i := len(layers) - 1; i >= 0; i-- {
		merged, found = mergeCodexTables(merged, layers[i]), true
	}
	if !found {
		return nil, fmt.Errorf("%w: %q is not in %s; pass --harness claude to search Claude Code's configuration",
			errServerNotFound, name, filepath.Join(codexHome, "config.toml"))
	}
	return codexServerFromTable(merged, name)
}

// trustedByCodex reports whether the global [projects] table trusts dir. The nearest directory
// that has an entry at all decides and the walk stops there, as codex's decision_for_dir returns
// the first matching entry: a directory the user marked "untrusted" is an answer, not a reason
// to keep looking at ancestors that might say otherwise.
//
// Codex keys the table by the path the project was opened at, so both the literal and the
// symlink-resolved spelling of each directory are tried, as localServers does. Both spellings
// name the same directory, so they are consulted together before the walk moves up a level.
//
// The walk stops at the first directory holding a .git entry, that directory included: codex
// consults the directory, the project root and the repo root, never every ancestor, and
// inheriting past a repo root would let a repository cloned into a trusted directory run the
// arbitrary command its own .codex/config.toml names. A subdirectory of a trusted repository
// still reaches the repo root, which is where the trust entry lives.
func trustedByCodex(global *codexConfig, dir string) bool {
	if global == nil {
		return false
	}
	for d := dir; ; {
		spellings := []string{d}
		if resolved, err := filepath.EvalSymlinks(d); err == nil && resolved != d {
			spellings = append(spellings, resolved)
		}
		for _, spelling := range spellings {
			if entry, ok := global.Projects[spelling]; ok {
				return entry.TrustLevel == "trusted"
			}
		}
		// .git is a directory in a normal checkout and a file in a worktree or submodule.
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return false
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false
		}
		d = parent
	}
}

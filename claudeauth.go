package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"
)

// keychainService is the macOS Keychain entry Claude Code stores its credentials in.
const keychainService = "Claude Code-credentials"

// oauthEntry is one MCP OAuth session inside Claude Code's credential blob.
type oauthEntry struct {
	ServerName     string `json:"serverName"`
	AccessToken    string `json:"accessToken"`
	RefreshToken   string `json:"refreshToken"`
	ExpiresAt      int64  `json:"expiresAt"`
	ClientID       string `json:"clientId"`
	Issuer         string `json:"issuer"`
	DiscoveryState struct {
		AuthorizationServerURL string `json:"authorizationServerUrl"`
	} `json:"discoveryState"`
}

func (e oauthEntry) expiry() time.Time { return time.UnixMilli(e.ExpiresAt) }

func (e oauthEntry) authorizationServer() string {
	if e.DiscoveryState.AuthorizationServerURL != "" {
		return e.DiscoveryState.AuthorizationServerURL
	}
	return e.Issuer
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

func writeCredentialsFile(data []byte) error {
	path, err := credentialsFile()
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".credentials-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

// The Keychain accessors are indirected so that tests can replace them: no test may read
// the real entry, send a real token to a test server, or write to the real store.
var (
	keychainCredentials      = readKeychain
	writeKeychainCredentials = writeKeychain
)

// readClaudeCredentials returns Claude Code's credential blob, or nil when there is none.
// macOS keeps it in the Keychain, which CLAUDE_CONFIG_DIR does not relocate; Linux and
// Windows keep it in a file. There is no cross-reading: on macOS a .credentials.json is
// one Claude Code itself treats as stale and deletes, so an unauthenticated call beats
// a token from it.
func readClaudeCredentials() ([]byte, error) {
	if !keychainAvailable {
		return readCredentialsFile()
	}
	data, err := keychainCredentials()
	if err != nil {
		return nil, nil
	}
	return data, nil
}

func writeClaudeCredentials(data []byte) error {
	if !keychainAvailable {
		return writeCredentialsFile(data)
	}
	return writeKeychainCredentials(data)
}

// findOAuthEntry returns the freshest session stored for serverName, along with the key it
// is filed under. Keys look like "<serverName>|<hash>", so the serverName field is matched
// instead of the key, and re-authorizing can leave an older entry behind.
func findOAuthEntry(credentials []byte, serverName string) (string, oauthEntry, bool) {
	var blob map[string]json.RawMessage
	if err := json.Unmarshal(credentials, &blob); err != nil {
		return "", oauthEntry{}, false
	}
	var sessions map[string]json.RawMessage
	if err := json.Unmarshal(blob["mcpOAuth"], &sessions); err != nil {
		return "", oauthEntry{}, false
	}
	var foundKey string
	var found oauthEntry
	for key, raw := range sessions {
		var entry oauthEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			continue
		}
		if entry.ServerName == serverName && entry.ExpiresAt >= found.ExpiresAt {
			foundKey, found = key, entry
		}
	}
	return foundKey, found, foundKey != ""
}

// storeRefreshedTokens writes the refreshed tokens back into the blob, touching only the
// four fields that changed. Everything else — other servers, the Claude Code login itself,
// fields this program does not model — is carried over verbatim, because dropping any of
// it would log the user out.
func storeRefreshedTokens(credentials []byte, key string, token *oauth2.Token) error {
	var blob map[string]json.RawMessage
	if err := json.Unmarshal(credentials, &blob); err != nil {
		return err
	}
	var sessions map[string]json.RawMessage
	if err := json.Unmarshal(blob["mcpOAuth"], &sessions); err != nil {
		return err
	}
	var entry map[string]any
	if err := json.Unmarshal(sessions[key], &entry); err != nil {
		return err
	}

	entry["accessToken"] = token.AccessToken
	entry["expiresAt"] = token.Expiry.UnixMilli()
	if token.RefreshToken != "" {
		entry["refreshToken"] = token.RefreshToken
	}

	updated, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	sessions[key] = updated
	if blob["mcpOAuth"], err = json.Marshal(sessions); err != nil {
		return err
	}
	data, err := json.Marshal(blob)
	if err != nil {
		return err
	}
	return writeClaudeCredentials(data)
}

// refreshOAuthEntry exchanges the stored refresh token for a fresh access token. Claude Code
// stores the result of its own refreshes the same way, and concurrent sessions of it already
// refresh against this store, so writing back is what keeps the two in step.
func refreshOAuthEntry(ctx context.Context, entry oauthEntry) (*oauth2.Token, error) {
	if entry.RefreshToken == "" || entry.ClientID == "" {
		return nil, fmt.Errorf("the stored session for %q has nothing to refresh with", entry.ServerName)
	}
	server := entry.authorizationServer()
	if server == "" {
		return nil, fmt.Errorf("the stored session for %q names no authorization server", entry.ServerName)
	}
	metadata, err := auth.GetAuthServerMetadata(ctx, server, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot discover the authorization server for %q: %w", entry.ServerName, err)
	}
	config := &oauth2.Config{
		ClientID: entry.ClientID,
		Endpoint: oauth2.Endpoint{TokenURL: metadata.TokenEndpoint, AuthStyle: oauth2.AuthStyleInParams},
	}
	token, err := config.TokenSource(ctx, &oauth2.Token{RefreshToken: entry.RefreshToken}).Token()
	if err != nil {
		return nil, fmt.Errorf("cannot refresh the OAuth token for %q: %w", entry.ServerName, err)
	}
	return token, nil
}

// oauthToken returns an access token for the named server, refreshing the stored one when it
// has expired, or an empty string when Claude Code holds no session for that server.
func oauthToken(ctx context.Context, credentials []byte, serverName string) (string, error) {
	if len(credentials) == 0 {
		return "", nil
	}
	// The credential format is not a documented interface. If a Claude Code update changes
	// it, fall through to an unauthenticated call and let the server report the problem.
	key, entry, found := findOAuthEntry(credentials, serverName)
	if !found || entry.AccessToken == "" {
		return "", nil
	}
	if time.Now().Before(entry.expiry()) {
		return entry.AccessToken, nil
	}
	token, err := refreshOAuthEntry(ctx, entry)
	if err != nil {
		return "", err
	}
	if err := storeRefreshedTokens(credentials, key, token); err != nil {
		return "", fmt.Errorf("refreshed the OAuth token for %q but could not store it: %w", serverName, err)
	}
	return token.AccessToken, nil
}

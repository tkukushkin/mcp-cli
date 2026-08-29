package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// authServer is a stand-in authorization server that answers metadata discovery and hands
// out one refreshed token.
type authServer struct {
	url          string
	refreshSeen  string
	accessToken  string
	refreshToken string
}

func newAuthServer(t *testing.T, accessToken, refreshToken string) *authServer {
	t.Helper()
	server := &authServer{accessToken: accessToken, refreshToken: refreshToken}
	mux := http.NewServeMux()
	metadata := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                           server.url,
			"authorization_endpoint":           server.url + "/authorize",
			"token_endpoint":                   server.url + "/token",
			"code_challenge_methods_supported": []string{"S256"},
		})
	}
	mux.HandleFunc("/.well-known/oauth-authorization-server", metadata)
	mux.HandleFunc("/.well-known/openid-configuration", metadata)
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		server.refreshSeen = r.Form.Get("refresh_token")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  server.accessToken,
			"refresh_token": server.refreshToken,
			"token_type":    "Bearer",
			"expires_in":    300,
		})
	})
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	server.url = httpServer.URL
	return server
}

func refreshableCredentials(serverName, authServerURL string, expiresAt time.Time) string {
	return `{
		"claudeAiOauth": {"accessToken": "claude-code-own-token", "scopes": ["user:inference"]},
		"mcpOAuth": {
			"` + serverName + `|abc123": {
				"serverName": "` + serverName + `",
				"serverUrl": "https://example.test/mcp",
				"accessToken": "stale-token",
				"refreshToken": "stored-refresh",
				"clientId": "3a8641ae",
				"issuer": "` + authServerURL + `",
				"scope": "offline_access",
				"discoveryState": {"authorizationServerUrl": "` + authServerURL + `", "oauthMetadataFound": true},
				"expiresAt": ` + strconv.FormatInt(expiresAt.UnixMilli(), 10) + `
			},
			"other|def456": {"serverName": "other", "accessToken": "untouched", "expiresAt": 1}
		}
	}`
}

// trackedStore points both credential stores at the given blob and reports what was written
// back, so a test reads the same way on macOS (Keychain) as on Linux and Windows (file).
func trackedStore(t *testing.T, credentials string) func() []byte {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	writeFile(t, path, credentials)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	fakeKeychain(t, []byte(credentials), nil)

	var written []byte
	original := writeKeychainCredentials
	t.Cleanup(func() { writeKeychainCredentials = original })
	writeKeychainCredentials = func(data []byte) error {
		written = data
		return nil
	}
	return func() []byte {
		if keychainAvailable {
			return written
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
}

func TestOAuthTokenRefreshesAnExpiredToken(t *testing.T) {
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	credentials := refreshableCredentials("mock", authorization.url, time.Now().Add(-time.Minute))
	trackedStore(t, credentials)

	token, err := oauthToken(t.Context(), []byte(credentials), "mock")
	if err != nil {
		t.Fatal(err)
	}
	if token != "fresh-token" {
		t.Errorf("got %q", token)
	}
	if authorization.refreshSeen != "stored-refresh" {
		t.Errorf("sent refresh token %q", authorization.refreshSeen)
	}
}

// Losing a field here would log the user out of Claude Code, so everything the program does
// not model has to survive the write.
func TestRefreshPreservesTheRestOfTheStore(t *testing.T) {
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	credentials := refreshableCredentials("mock", authorization.url, time.Now().Add(-time.Minute))
	stored := trackedStore(t, credentials)

	if _, err := oauthToken(t.Context(), []byte(credentials), "mock"); err != nil {
		t.Fatal(err)
	}

	var blob map[string]any
	if err := json.Unmarshal(stored(), &blob); err != nil {
		t.Fatal(err)
	}
	claude, ok := blob["claudeAiOauth"].(map[string]any)
	if !ok || claude["accessToken"] != "claude-code-own-token" {
		t.Fatalf("the Claude Code login did not survive: %v", blob["claudeAiOauth"])
	}
	if scopes, ok := claude["scopes"].([]any); !ok || len(scopes) != 1 {
		t.Errorf("nested fields did not survive: %v", claude["scopes"])
	}

	sessions := blob["mcpOAuth"].(map[string]any)
	other := sessions["other|def456"].(map[string]any)
	if other["accessToken"] != "untouched" {
		t.Errorf("another server's session changed: %v", other)
	}

	entry := sessions["mock|abc123"].(map[string]any)
	if entry["accessToken"] != "fresh-token" || entry["refreshToken"] != "rotated-refresh" {
		t.Errorf("the refreshed tokens were not stored: %v", entry)
	}
	if entry["clientId"] != "3a8641ae" || entry["scope"] != "offline_access" {
		t.Errorf("unmodelled fields of the entry were dropped: %v", entry)
	}
	if entry["serverUrl"] != "https://example.test/mcp" {
		t.Errorf("serverUrl was dropped: %v", entry)
	}
	expiresAt, ok := entry["expiresAt"].(float64)
	if !ok || time.UnixMilli(int64(expiresAt)).Before(time.Now()) {
		t.Errorf("expiry was not moved forward: %v", entry["expiresAt"])
	}
}

func TestRefreshReportsAStoreFailure(t *testing.T) {
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	credentials := refreshableCredentials("mock", authorization.url, time.Now().Add(-time.Minute))
	trackedStore(t, credentials)
	writeKeychainCredentials = func([]byte) error { return io.ErrClosedPipe }
	if !keychainAvailable {
		t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "missing"))
	}

	_, err := oauthToken(t.Context(), []byte(credentials), "mock")
	if err == nil || !strings.Contains(err.Error(), "could not store it") {
		t.Fatalf("got %v, want a store failure", err)
	}
}

func TestRefreshReportsAnAuthorizationServerFailure(t *testing.T) {
	credentials := refreshableCredentials("mock", "https://127.0.0.1:1", time.Now().Add(-time.Minute))
	trackedStore(t, credentials)

	_, err := oauthToken(t.Context(), []byte(credentials), "mock")
	if err == nil || !strings.Contains(err.Error(), "authorization server") {
		t.Fatalf("got %v, want a discovery failure", err)
	}
}

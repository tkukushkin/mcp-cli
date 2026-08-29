package main

import (
	"context"
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
	// expiresIn is left out of the token response when it is zero, which RFC 6749 allows.
	expiresIn int
}

func newAuthServer(t *testing.T, accessToken, refreshToken string) *authServer {
	t.Helper()
	server := &authServer{accessToken: accessToken, refreshToken: refreshToken, expiresIn: 300}
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
		response := map[string]any{
			"access_token":  server.accessToken,
			"refresh_token": server.refreshToken,
			"token_type":    "Bearer",
		}
		if server.expiresIn > 0 {
			response["expires_in"] = server.expiresIn
		}
		json.NewEncoder(w).Encode(response)
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

// trackedStore backs both credential stores with one file and reports its contents, so a test
// reads and writes the same way on macOS (Keychain) as on Linux and Windows (file).
func trackedStore(t *testing.T, credentials string) func() []byte {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	writeFile(t, path, credentials)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	stored := func() []byte {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	fakeKeychain(t, nil, nil)
	keychainCredentials = func(context.Context) ([]byte, error) { return stored(), nil }
	original := writeKeychainCredentials
	t.Cleanup(func() { writeKeychainCredentials = original })
	writeKeychainCredentials = func(_ context.Context, data []byte) error { return os.WriteFile(path, data, 0o600) }
	return stored
}

func TestOAuthTokenRefreshesAnExpiredToken(t *testing.T) {
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	credentials := refreshableCredentials("mock", authorization.url, time.Now().Add(-time.Minute))
	trackedStore(t, credentials)

	token, err := oauthToken(t.Context(), []byte(credentials), "mock", "")
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

	if _, err := oauthToken(t.Context(), []byte(credentials), "mock", ""); err != nil {
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

// The refresh takes two round trips, and a Claude Code session may write the store during
// them. Writing the whole blob back from the copy this run started with would revert that.
func TestRefreshWritesBackOntoTheCurrentStore(t *testing.T) {
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	stale := refreshableCredentials("mock", authorization.url, time.Now().Add(-time.Minute))
	stored := trackedStore(t, strings.Replace(stale, "claude-code-own-token", "rotated-elsewhere", 1))

	if _, err := oauthToken(t.Context(), []byte(stale), "mock", ""); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(stored()), "rotated-elsewhere") {
		t.Error("the write-back reverted a concurrent change to the store")
	}
	if !strings.Contains(string(stored()), "fresh-token") {
		t.Error("the refreshed token was not stored")
	}
}

// A token endpoint may leave expires_in out; storing the resulting zero expiry would write a
// date in year 1, which leaves the entry expired and unfindable forever after.
func TestRefreshWithoutAnExpiryKeepsTheStoredOne(t *testing.T) {
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	authorization.expiresIn = 0
	expiresAt := time.Now().Add(-time.Minute)
	credentials := refreshableCredentials("mock", authorization.url, expiresAt)
	stored := trackedStore(t, credentials)

	if _, err := oauthToken(t.Context(), []byte(credentials), "mock", ""); err != nil {
		t.Fatal(err)
	}

	var blob struct {
		MCPOAuth map[string]oauthEntry `json:"mcpOAuth"`
	}
	if err := json.Unmarshal(stored(), &blob); err != nil {
		t.Fatal(err)
	}
	if got := blob.MCPOAuth["mock|abc123"].ExpiresAt; got != expiresAt.UnixMilli() {
		t.Errorf("expiresAt = %d, want the stored %d", got, expiresAt.UnixMilli())
	}
}

// A token with seconds left would expire mid-handshake, so it is refreshed instead of sent.
func TestOAuthTokenRefreshesATokenAboutToExpire(t *testing.T) {
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	credentials := refreshableCredentials("mock", authorization.url, time.Now().Add(2*time.Second))
	trackedStore(t, credentials)

	token, err := oauthToken(t.Context(), []byte(credentials), "mock", "")
	if err != nil {
		t.Fatal(err)
	}
	if token != "fresh-token" {
		t.Errorf("got %q, want the refreshed token", token)
	}
}

func TestRefreshReportsAStoreFailure(t *testing.T) {
	withKeychain(t, true)
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	credentials := refreshableCredentials("mock", authorization.url, time.Now().Add(-time.Minute))
	trackedStore(t, credentials)
	writeKeychainCredentials = func(context.Context, []byte) error { return io.ErrClosedPipe }

	_, err := oauthToken(t.Context(), []byte(credentials), "mock", "")
	if err == nil || !strings.Contains(err.Error(), "could not store it") {
		t.Fatalf("got %v, want a store failure", err)
	}
}

func TestRefreshReportsAnAuthorizationServerFailure(t *testing.T) {
	credentials := refreshableCredentials("mock", "https://127.0.0.1:1", time.Now().Add(-time.Minute))
	trackedStore(t, credentials)

	_, err := oauthToken(t.Context(), []byte(credentials), "mock", "")
	if err == nil || !strings.Contains(err.Error(), "authorization server") {
		t.Fatalf("got %v, want a discovery failure", err)
	}
}

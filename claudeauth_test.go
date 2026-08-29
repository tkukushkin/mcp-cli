package main

import (
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func credentialsJSON(serverName string, expiresAt time.Time, token string) string {
	return `{"mcpOAuth": {"` + serverName + `|abc123": {
		"serverName": "` + serverName + `",
		"accessToken": "` + token + `",
		"expiresAt": ` + strconv.FormatInt(expiresAt.UnixMilli(), 10) + `
	}}}`
}

func TestOAuthTokenFindsAValidToken(t *testing.T) {
	credentials := credentialsJSON("fastmail", time.Now().Add(time.Hour), "live-token")

	token, err := oauthToken(t.Context(), []byte(credentials), "fastmail")
	if err != nil {
		t.Fatal(err)
	}
	if token != "live-token" {
		t.Errorf("got %q", token)
	}
}

func TestOAuthTokenIgnoresOtherServers(t *testing.T) {
	credentials := credentialsJSON("fastmail", time.Now().Add(time.Hour), "live-token")

	token, err := oauthToken(t.Context(), []byte(credentials), "context7")
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		t.Errorf("got %q, want no token", token)
	}
}

// An expired token is refreshed, so an entry with nothing to refresh with is a dead end.
func TestOAuthTokenReportsAnUnrefreshableEntry(t *testing.T) {
	credentials := credentialsJSON("fastmail", time.Now().Add(-time.Minute), "stale-token")

	_, err := oauthToken(t.Context(), []byte(credentials), "fastmail")
	if err == nil {
		t.Fatal("expected an error for an entry that cannot be refreshed")
	}
	if !strings.Contains(err.Error(), "nothing to refresh with") {
		t.Errorf("got %v", err)
	}
}

// Re-authorizing leaves the old entry behind, so the freshest expiry wins.
func TestOAuthTokenPrefersTheFreshestEntry(t *testing.T) {
	credentials := `{"mcpOAuth": {
		"fastmail|old": {"serverName": "fastmail", "accessToken": "old", "expiresAt": ` + strconv.FormatInt(time.Now().Add(time.Minute).UnixMilli(), 10) + `},
		"fastmail|new": {"serverName": "fastmail", "accessToken": "new", "expiresAt": ` + strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10) + `}
	}}`

	token, err := oauthToken(t.Context(), []byte(credentials), "fastmail")
	if err != nil {
		t.Fatal(err)
	}
	if token != "new" {
		t.Errorf("got %q", token)
	}
}

func TestOAuthTokenWithoutCredentials(t *testing.T) {
	token, err := oauthToken(t.Context(), nil, "fastmail")
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		t.Errorf("got %q, want no token", token)
	}
}

// A Claude Code update may change the format; an unauthenticated call beats a hard failure.
func TestOAuthTokenIgnoresMalformedCredentials(t *testing.T) {
	token, err := oauthToken(t.Context(), []byte(`{"mcpOAuth": `), "fastmail")
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		t.Errorf("got %q, want no token", token)
	}
}

func TestCredentialsFileHonoursClaudeConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	path, err := credentialsFile()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, ".credentials.json") {
		t.Errorf("got %q", path)
	}
}

func TestCredentialsFileFallsBackToTheClaudeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("HOME", home)

	path, err := credentialsFile()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, ".claude", ".credentials.json") {
		t.Errorf("got %q", path)
	}
}

func TestReadCredentialsFileWithoutAFile(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	credentials, err := readCredentialsFile()
	if err != nil {
		t.Fatal(err)
	}
	if credentials != nil {
		t.Errorf("got %q, want nil", credentials)
	}
}

func fakeKeychain(t *testing.T, credentials []byte, err error) {
	t.Helper()
	original := keychainCredentials
	t.Cleanup(func() { keychainCredentials = original })
	keychainCredentials = func() ([]byte, error) { return credentials, err }
}

// CLAUDE_CONFIG_DIR relocates .credentials.json on Linux and Windows, but macOS keeps
// credentials in the Keychain regardless, so there the Keychain must still win.
func TestReadClaudeCredentialsPrefersTheKeychainOnMacOS(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".credentials.json"),
		credentialsJSON("mock", time.Now().Add(time.Hour), "from-file"))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	fakeKeychain(t, []byte(credentialsJSON("mock", time.Now().Add(time.Hour), "from-keychain")), nil)

	credentials, err := readClaudeCredentials()
	if err != nil {
		t.Fatal(err)
	}
	token, err := oauthToken(t.Context(), credentials, "mock")
	if err != nil {
		t.Fatal(err)
	}
	want := "from-file"
	if runtime.GOOS == "darwin" {
		want = "from-keychain"
	}
	if token != want {
		t.Errorf("got %q, want %q on %s", token, want, runtime.GOOS)
	}
}

// Without a Keychain entry macOS reports no credentials rather than reading a file that
// Claude Code would have deleted; other platforms read the file, which is their only store.
func TestReadClaudeCredentialsWithoutAKeychainEntry(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".credentials.json"),
		credentialsJSON("mock", time.Now().Add(time.Hour), "from-file"))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	fakeKeychain(t, nil, errors.New("no keychain entry"))

	credentials, err := readClaudeCredentials()
	if err != nil {
		t.Fatal(err)
	}
	token, err := oauthToken(t.Context(), credentials, "mock")
	if err != nil {
		t.Fatal(err)
	}
	want := "from-file"
	if runtime.GOOS == "darwin" {
		want = ""
	}
	if token != want {
		t.Errorf("got %q, want %q on %s", token, want, runtime.GOOS)
	}
}

// useCredentials fills both stores, so a test reads the same blob on macOS (Keychain)
// as on Linux and Windows (file).
func useCredentials(t *testing.T, credentials string) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".credentials.json"), credentials)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	fakeKeychain(t, []byte(credentials), nil)
}

// withCredentials gives Claude Code a live token for serverName.
func withCredentials(t *testing.T, serverName, token string) {
	t.Helper()
	useCredentials(t, credentialsJSON(serverName, time.Now().Add(time.Hour), token))
}

func TestAuthorizeAddsTheStoredToken(t *testing.T) {
	withCredentials(t, "mock", "stored-token")

	headers, err := authorize(t.Context(), &serverConfig{Name: "mock", URL: "https://example.test/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if headers["Authorization"] != "Bearer stored-token" {
		t.Errorf("got %q", headers["Authorization"])
	}
}

func TestAuthorizeKeepsAnExplicitHeader(t *testing.T) {
	withCredentials(t, "mock", "stored-token")
	cfg := &serverConfig{Name: "mock", URL: "https://example.test/mcp",
		Headers: map[string]string{"Authorization": "Bearer from-config"}}

	headers, err := authorize(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if headers["Authorization"] != "Bearer from-config" {
		t.Errorf("got %q, want the header from the server config", headers["Authorization"])
	}
}

func TestAuthorizeLeavesOtherHeadersAlone(t *testing.T) {
	withCredentials(t, "mock", "stored-token")
	cfg := &serverConfig{Name: "mock", URL: "https://example.test/mcp",
		Headers: map[string]string{"X-Trace": "abc"}}

	headers, err := authorize(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if headers["X-Trace"] != "abc" || headers["Authorization"] != "Bearer stored-token" {
		t.Errorf("got %v", headers)
	}
	if _, mutated := cfg.Headers["Authorization"]; mutated {
		t.Error("the server config was mutated")
	}
}

func TestAuthorizeWithoutAStoredSession(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	headers, err := authorize(t.Context(), &serverConfig{Name: "mock", URL: "https://example.test/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 0 {
		t.Errorf("got %v, want no headers", headers)
	}
}

func TestCallToolSendsTheStoredToken(t *testing.T) {
	withCredentials(t, "mock", "stored-token")
	cfg, recorder := httpConfig(t, nil)
	cfg.Name = "mock"

	if _, err := callTool(t.Context(), cfg, "echo", map[string]any{"message": "x"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := recorder.get("Authorization"); got != "Bearer stored-token" {
		t.Errorf("Authorization header = %q", got)
	}
}

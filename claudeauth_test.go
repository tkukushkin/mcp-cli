package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
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

	token, err := oauthToken(t.Context(), []byte(credentials), "fastmail", "")
	if err != nil {
		t.Fatal(err)
	}
	if token != "live-token" {
		t.Errorf("got %q", token)
	}
}

func TestOAuthTokenIgnoresOtherServers(t *testing.T) {
	credentials := credentialsJSON("fastmail", time.Now().Add(time.Hour), "live-token")

	token, err := oauthToken(t.Context(), []byte(credentials), "context7", "")
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

	_, err := oauthToken(t.Context(), []byte(credentials), "fastmail", "")
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

	token, err := oauthToken(t.Context(), []byte(credentials), "fastmail", "")
	if err != nil {
		t.Fatal(err)
	}
	if token != "new" {
		t.Errorf("got %q", token)
	}
}

// Two projects can name different servers the same, and the token of one must not be sent
// to the other.
func TestOAuthTokenIgnoresASessionForAnotherURL(t *testing.T) {
	credentials := `{"mcpOAuth": {"github|abc": {
		"serverName": "github",
		"serverUrl": "https://other.test/mcp",
		"accessToken": "other-projects-token",
		"expiresAt": ` + strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10) + `
	}}}`

	token, err := oauthToken(t.Context(), []byte(credentials), "github", "https://example.test/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		t.Errorf("got %q, want no token for a session of another server", token)
	}
}

func TestOAuthTokenAcceptsTheSameURL(t *testing.T) {
	credentials := `{"mcpOAuth": {"github|abc": {
		"serverName": "github",
		"serverUrl": "https://example.test/mcp/",
		"accessToken": "this-projects-token",
		"expiresAt": ` + strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10) + `
	}}}`

	token, err := oauthToken(t.Context(), []byte(credentials), "github", "https://example.test/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if token != "this-projects-token" {
		t.Errorf("got %q", token)
	}
}

func TestOAuthTokenWithoutCredentials(t *testing.T) {
	token, err := oauthToken(t.Context(), nil, "fastmail", "")
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		t.Errorf("got %q, want no token", token)
	}
}

// A Claude Code update may change the format; an unauthenticated call beats a hard failure.
func TestOAuthTokenIgnoresMalformedCredentials(t *testing.T) {
	token, err := oauthToken(t.Context(), []byte(`{"mcpOAuth": `), "fastmail", "")
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
	setHome(t, home)

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
	keychainCredentials = func(context.Context) ([]byte, error) { return credentials, err }
}

// withKeychain fixes which store answers, so both are exercised wherever the tests run.
func withKeychain(t *testing.T, available bool) {
	t.Helper()
	original := keychainAvailable
	t.Cleanup(func() { keychainAvailable = original })
	keychainAvailable = available
}

// CLAUDE_CONFIG_DIR relocates .credentials.json, but where the Keychain is the store it is
// the Keychain that answers.
func TestReadClaudeCredentialsPrefersTheKeychain(t *testing.T) {
	withKeychain(t, true)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".credentials.json"),
		credentialsJSON("mock", time.Now().Add(time.Hour), "from-file"))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	fakeKeychain(t, []byte(credentialsJSON("mock", time.Now().Add(time.Hour), "from-keychain")), nil)

	token, err := storedToken(t, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if token != "from-keychain" {
		t.Errorf("got %q", token)
	}
}

func TestReadClaudeCredentialsReadsTheFileWithoutAKeychain(t *testing.T) {
	withKeychain(t, false)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".credentials.json"),
		credentialsJSON("mock", time.Now().Add(time.Hour), "from-file"))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	token, err := storedToken(t, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if token != "from-file" {
		t.Errorf("got %q", token)
	}
}

// Without a Keychain entry there is no falling back to a file that Claude Code would have
// deleted: the call goes out unauthenticated instead.
func TestReadClaudeCredentialsWithoutAKeychainEntry(t *testing.T) {
	withKeychain(t, true)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".credentials.json"),
		credentialsJSON("mock", time.Now().Add(time.Hour), "from-file"))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	fakeKeychain(t, nil, errKeychainNoEntry)

	token, err := storedToken(t, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		t.Errorf("got %q, want no token", token)
	}
}

// A locked keychain or a denied prompt is not "no credentials": degrading it to an
// unauthenticated call hides the real cause behind the server's 401.
func TestReadClaudeCredentialsReportsAKeychainFailure(t *testing.T) {
	withKeychain(t, true)
	fakeKeychain(t, nil, errors.New("User interaction is not allowed"))

	if _, err := readClaudeCredentials(t.Context()); err == nil {
		t.Fatal("expected a Keychain read failure to surface")
	}
}

func storedToken(t *testing.T, serverName string) (string, error) {
	t.Helper()
	credentials, err := readClaudeCredentials(t.Context())
	if err != nil {
		return "", err
	}
	return oauthToken(t.Context(), credentials, serverName, "")
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

// HTTP header names ignore case, so a lowercase one in the config is still the user's own
// Authorization header and must not be joined by a second one.
func TestAuthorizeKeepsAnExplicitHeaderWhateverItsCase(t *testing.T) {
	withCredentials(t, "mock", "stored-token")
	cfg := &serverConfig{Name: "mock", URL: "https://example.test/mcp",
		Headers: map[string]string{"authorization": "Bearer from-config"}}

	headers, err := authorize(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 1 || headers["authorization"] != "Bearer from-config" {
		t.Errorf("got %v, want only the header from the server config", headers)
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

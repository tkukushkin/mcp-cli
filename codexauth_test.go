package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// keyringEntry addresses one keyring entry the way the OS keyrings do, by service and account.
type keyringEntry struct{ service, account string }

// fakeCodexKeyring replaces the keyring accessors with a map, so no test reads or writes a
// real keyring. The map it returns is the store, and reading it shows what was written.
func fakeCodexKeyring(t *testing.T, entries map[keyringEntry]string) map[keyringEntry]string {
	t.Helper()
	get, set := codexKeyringGet, codexKeyringSet
	t.Cleanup(func() { codexKeyringGet, codexKeyringSet = get, set })

	store := map[keyringEntry]string{}
	for entry, value := range entries {
		store[entry] = value
	}
	codexKeyringGet = func(_ context.Context, service, account string) (string, error) {
		return store[keyringEntry{service, account}], nil
	}
	codexKeyringSet = func(_ context.Context, service, account, value string) error {
		store[keyringEntry{service, account}] = value
		return nil
	}
	return store
}

// codexRecord builds the token record codex stores in the keyring and in the secrets file,
// with two fields — token_type and scope — this program does not model and must not drop.
func codexRecord(serverName, serverURL, issuer string, expiresAt int64) string {
	record := fmt.Sprintf(`{"server_name":%q,"url":%q,"issuer":%q,"client_id":"cid",`+
		`"token_response":{"access_token":"stale-token","token_type":"bearer",`+
		`"refresh_token":"stored-refresh","scope":"offline_access"}`,
		serverName, serverURL, issuer)
	if expiresAt != 0 {
		record += fmt.Sprintf(`,"expires_at":%d`, expiresAt)
	}
	return record + "}"
}

// codexFallbackEntry builds one entry of $CODEX_HOME/.credentials.json, again with a field
// (scopes) the program does not model.
func codexFallbackEntry(serverName, serverURL, issuer string, expiresAt int64) string {
	entry := fmt.Sprintf(`{"server_name":%q,"server_url":%q,"issuer":%q,"client_id":"cid",`+
		`"access_token":"stale-token","refresh_token":"stored-refresh","scopes":["offline_access"]`,
		serverName, serverURL, issuer)
	if expiresAt != 0 {
		entry += fmt.Sprintf(`,"expires_at":%d`, expiresAt)
	}
	return entry + "}"
}

func expiredMillis() int64 { return time.Now().Add(-time.Minute).UnixMilli() }

const codexServerURL = "https://example.com/mcp"

// The store key addresses codex's direct keyring entries, so the payload it hashes has to be
// byte-identical to codex's. The hashes are golden values, computed once with
//
//	printf '{"type":"http","url":"<url>","headers":{}}' | shasum -a 256 | cut -c1-16
//
// rather than re-derived here, where a payload the implementation and the test agree on but
// codex does not would pass.
func TestCodexStoreKey(t *testing.T) {
	if key := codexStoreKey("srv", codexServerURL); key != "srv|9a70b85417749d5c" {
		t.Errorf("codexStoreKey = %q", key)
	}
	// A query string is the case a JSON encoder that escapes & for HTML would get wrong.
	if key := codexStoreKey("srv", "https://example.com/mcp?a=1&b=2"); key != "srv|495a0d1a599a095d" {
		t.Errorf("codexStoreKey with a query = %q", key)
	}
	// codex strips the local: prefix its own server names carry before hashing.
	if key := codexStoreKey("local:srv", codexServerURL); key != "srv|9a70b85417749d5c" {
		t.Errorf("codexStoreKey of a local: name = %q", key)
	}
}

// Golden value: printf 'srv|9a70b85417749d5c' | shasum -a 256 | cut -c1-32 | tr 'a-f' 'A-F'
func TestCodexSecretName(t *testing.T) {
	name := codexSecretName(codexStoreKey("srv", codexServerURL))
	if name != "MCP_OAUTH_848DF9D2D3142B56362B53C576AD91DB" {
		t.Errorf("codexSecretName = %q", name)
	}
}

// Golden value: printf '/nonexistent/codex-home' | shasum -a 256 | cut -c1-16
// The path is one that does not exist, so no symlink resolution changes it.
func TestCodexPassphraseAccount(t *testing.T) {
	account := codexPassphraseAccount("/nonexistent/codex-home")
	if account != "secrets|b5d85b424b15c4d9" {
		t.Errorf("codexPassphraseAccount = %q", account)
	}
}

func TestCodexSecretsFileRoundTrip(t *testing.T) {
	codexHome := t.TempDir()
	passphrase := "dGVzdC1wYXNzcGhyYXNl"
	tokens := codexRecord("srv", codexServerURL, "https://issuer.example.com", 1)
	key := "global/" + codexSecretName(codexStoreKey("srv", codexServerURL))
	if err := writeCodexSecretsFile(codexHome, passphrase, map[string]string{key: tokens}); err != nil {
		t.Fatal(err)
	}

	secrets, err := readCodexSecretsFile(codexHome, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	storedKey, entry, found := findCodexEntry(secrets, "srv", codexServerURL)
	if !found || entry.accessToken() != "stale-token" || entry.refreshToken() != "stored-refresh" {
		t.Fatalf("round trip lost the entry: %+v, %v", entry, found)
	}
	if storedKey != key {
		t.Errorf("entry found under %q, want %q", storedKey, key)
	}
}

// A wrong passphrase is a failure to read a store that exists, not an absent session.
func TestCodexSecretsFileReportsABadPassphrase(t *testing.T) {
	codexHome := t.TempDir()
	if err := writeCodexSecretsFile(codexHome, "right", map[string]string{"global/X": "{}"}); err != nil {
		t.Fatal(err)
	}
	if _, err := readCodexSecretsFile(codexHome, "wrong"); err == nil {
		t.Fatal("a wrong passphrase decrypted the file")
	}
}

// codex files entries under a key derived from the URL, but matches them on the fields, so an
// entry written for a different name or by an older codex is still found — and an executor's
// entry never is.
func TestFindCodexEntryMatchesTheServer(t *testing.T) {
	secrets := map[string]string{
		"global/MCP_OAUTH_STALEKEY": codexRecord("srv", codexServerURL, "", 1),
		"global/MCP_OAUTH_OTHER":    codexRecord("other", "https://other.example.com/mcp", "", 1),
		"unrelated":                 codexRecord("srv", codexServerURL, "", 1),
	}
	key, entry, found := findCodexEntry(secrets, "srv", codexServerURL)
	if !found || entry.ServerName != "srv" || key != "global/MCP_OAUTH_STALEKEY" {
		t.Fatalf("entry not found under an unexpected key: %q, %+v, %v", key, entry, found)
	}
	if _, _, found := findCodexEntry(secrets, "srv", "https://elsewhere.example.com/mcp"); found {
		t.Error("an entry for another URL matched")
	}
	if _, _, found := findCodexEntry(map[string]string{"global/MCP_OAUTH_X": "not json"}, "srv", codexServerURL); found {
		t.Error("an unparsable record matched")
	}
}

func codexFallbackFile(t *testing.T, codexHome string, entries map[string]string) {
	t.Helper()
	var pairs []string
	for key, entry := range entries {
		pairs = append(pairs, fmt.Sprintf("%q:%s", key, entry))
	}
	writeFile(t, filepath.Join(codexHome, ".credentials.json"), "{"+strings.Join(pairs, ",")+"}")
}

func codexHomeWithFallback(t *testing.T, entries map[string]string) string {
	t.Helper()
	codexHome := t.TempDir()
	fakeCodexKeyring(t, nil)
	codexFallbackFile(t, codexHome, entries)
	return codexHome
}

// An executor_owned entry belongs to codex's remote executor; codex skips it for host lookups
// and so does this, or a call would go out with a token minted for someone else. The entry is
// one that matches the server in every other way, so only the marker keeps it out.
func TestCodexOAuthTokenSkipsExecutorEntries(t *testing.T) {
	codexHome := codexHomeWithFallback(t, map[string]string{
		"srv|abc": strings.Replace(
			codexFallbackEntry("srv", codexServerURL, "", 0), `"client_id"`, `"executor_owned":true,"client_id"`, 1),
	})

	token, err := codexOAuthToken(t.Context(), "srv", codexServerURL, codexHome)
	if err != nil || token != "" {
		t.Fatalf("token = %q, err = %v, want no session", token, err)
	}
}

// An absent expires_at means the token does not expire, as it does for codex's own
// token_needs_refresh. Refreshing anyway would spend a single-use refresh token on every call.
func TestCodexOAuthTokenWithoutAnExpiryDoesNotRefresh(t *testing.T) {
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	codexHome := codexHomeWithFallback(t, map[string]string{
		"srv|abc": codexFallbackEntry("srv", codexServerURL, authorization.url, 0),
	})

	token, err := codexOAuthToken(t.Context(), "srv", codexServerURL, codexHome)
	if err != nil {
		t.Fatal(err)
	}
	if token != "stale-token" {
		t.Errorf("token = %q, want the stored one", token)
	}
	if authorization.refreshSeen != "" {
		t.Error("a token without an expiry was refreshed")
	}
}

// Dropping a field of another server's entry would log the user out of it.
func TestCodexRefreshFromTheFallbackFileKeepsTheRestOfTheFile(t *testing.T) {
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	other := codexFallbackEntry("other", "https://other.example.com/mcp", "", 1)
	codexHome := codexHomeWithFallback(t, map[string]string{
		"srv|abc":   codexFallbackEntry("srv", codexServerURL, authorization.url, expiredMillis()),
		"other|def": other,
	})

	token, err := codexOAuthToken(t.Context(), "srv", codexServerURL, codexHome)
	if err != nil {
		t.Fatal(err)
	}
	if token != "fresh-token" {
		t.Errorf("token = %q, want the refreshed one", token)
	}
	if authorization.refreshSeen != "stored-refresh" {
		t.Errorf("sent refresh token %q", authorization.refreshSeen)
	}

	var stored map[string]map[string]any
	data, err := os.ReadFile(filepath.Join(codexHome, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	entry := stored["srv|abc"]
	if entry["access_token"] != "fresh-token" || entry["refresh_token"] != "rotated-refresh" {
		t.Errorf("the refreshed tokens were not stored: %v", entry)
	}
	if expiresAt, ok := entry["expires_at"].(float64); !ok || time.UnixMilli(int64(expiresAt)).Before(time.Now()) {
		t.Errorf("expiry was not moved forward: %v", entry["expires_at"])
	}
	if scopes, ok := entry["scopes"].([]any); !ok || len(scopes) != 1 {
		t.Errorf("unmodelled fields of the entry were dropped: %v", entry)
	}

	var untouched map[string]any
	if err := json.Unmarshal([]byte(other), &untouched); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(stored["other|def"]) != fmt.Sprint(untouched) {
		t.Errorf("another server's entry changed: %v", stored["other|def"])
	}
}

func TestCodexRefreshFromTheSecretsFile(t *testing.T) {
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	codexHome := t.TempDir()
	passphrase := "dGVzdC1wYXNzcGhyYXNl"
	fakeCodexKeyring(t, map[keyringEntry]string{
		{"codex", codexPassphraseAccount(codexHome)}: passphrase,
	})
	key := "global/" + codexSecretName(codexStoreKey("srv", codexServerURL))
	secrets := map[string]string{
		key:                        codexRecord("srv", codexServerURL, authorization.url, expiredMillis()),
		"global/MCP_OAUTH_UNTOUCH": codexRecord("other", "https://other.example.com/mcp", "", 1),
	}
	if err := writeCodexSecretsFile(codexHome, passphrase, secrets); err != nil {
		t.Fatal(err)
	}

	token, err := codexOAuthToken(t.Context(), "srv", codexServerURL, codexHome)
	if err != nil {
		t.Fatal(err)
	}
	if token != "fresh-token" {
		t.Errorf("token = %q, want the refreshed one", token)
	}

	stored, err := readCodexSecretsFile(codexHome, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if stored["global/MCP_OAUTH_UNTOUCH"] != secrets["global/MCP_OAUTH_UNTOUCH"] {
		t.Errorf("another server's secret changed: %v", stored["global/MCP_OAUTH_UNTOUCH"])
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(stored[key]), &record); err != nil {
		t.Fatal(err)
	}
	response, _ := record["token_response"].(map[string]any)
	if response["access_token"] != "fresh-token" || response["refresh_token"] != "rotated-refresh" {
		t.Errorf("the refreshed tokens were not stored: %v", record)
	}
	if response["token_type"] != "bearer" || response["scope"] != "offline_access" {
		t.Errorf("unmodelled fields of the token response were dropped: %v", record)
	}
	if expiresAt, ok := record["expires_at"].(float64); !ok || time.UnixMilli(int64(expiresAt)).Before(time.Now()) {
		t.Errorf("expiry was not moved forward: %v", record["expires_at"])
	}
}

func TestCodexRefreshFromTheDirectKeyring(t *testing.T) {
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	codexHome := t.TempDir()
	account := codexStoreKey("srv", codexServerURL)
	store := fakeCodexKeyring(t, map[keyringEntry]string{
		{codexKeyringService, account}: codexRecord("srv", codexServerURL, authorization.url, expiredMillis()),
	})

	token, err := codexOAuthToken(t.Context(), "srv", codexServerURL, codexHome)
	if err != nil {
		t.Fatal(err)
	}
	if token != "fresh-token" {
		t.Errorf("token = %q, want the refreshed one", token)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(store[keyringEntry{codexKeyringService, account}]), &record); err != nil {
		t.Fatal(err)
	}
	response, _ := record["token_response"].(map[string]any)
	if response["access_token"] != "fresh-token" || response["scope"] != "offline_access" {
		t.Errorf("the keyring entry was not updated in place: %v", record)
	}
}

// The store format is not a documented interface: a record this program cannot parse falls
// through to an unauthenticated call, the way the Claude Code path does.
func TestCodexOAuthTokenIgnoresAnUnparsableStore(t *testing.T) {
	codexHome := t.TempDir()
	fakeCodexKeyring(t, map[keyringEntry]string{
		{codexKeyringService, codexStoreKey("srv", codexServerURL)}: "not a token record",
	})
	writeFile(t, filepath.Join(codexHome, ".credentials.json"), "not json either")

	token, err := codexOAuthToken(t.Context(), "srv", codexServerURL, codexHome)
	if err != nil || token != "" {
		t.Fatalf("token = %q, err = %v, want an unauthenticated call", token, err)
	}
}

// A keyring that cannot be read is not proof that there is no session — codex falls back to
// the file for exactly this case — but if nothing else holds one, the failure is the answer.
func TestCodexOAuthTokenFallsBackFromAFailingKeyring(t *testing.T) {
	codexHome := t.TempDir()
	get := codexKeyringGet
	t.Cleanup(func() { codexKeyringGet = get })
	codexKeyringGet = func(context.Context, string, string) (string, error) {
		return "", errors.New("the keychain is locked")
	}
	codexFallbackFile(t, codexHome, map[string]string{
		"srv|abc": codexFallbackEntry("srv", codexServerURL, "", 0),
	})

	token, err := codexOAuthToken(t.Context(), "srv", codexServerURL, codexHome)
	if err != nil || token != "stale-token" {
		t.Fatalf("token = %q, err = %v, want the file's session", token, err)
	}

	if err := os.Remove(filepath.Join(codexHome, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := codexOAuthToken(t.Context(), "srv", codexServerURL, codexHome); err == nil {
		t.Fatal("an unreadable keyring degraded to an unauthenticated call")
	}
}

// authorize routes by the harness the config came from: a Codex server takes its token from
// codex's stores, and a Claude Code server must not.
func TestAuthorizeUsesTheCodexStoresForACodexServer(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	codexHome := filepath.Join(home, ".codex")
	fakeCodexKeyring(t, nil)
	codexFallbackFile(t, codexHome, map[string]string{
		"srv|abc": codexFallbackEntry("srv", codexServerURL, "", 0),
	})

	headers, err := authorize(t.Context(), &serverConfig{Name: "srv", URL: codexServerURL, harness: harnessCodex})
	if err != nil {
		t.Fatal(err)
	}
	if headers["Authorization"] != "Bearer stale-token" {
		t.Errorf("Authorization = %q, want codex's stored token", headers["Authorization"])
	}

	headers, err = authorize(t.Context(), &serverConfig{Name: "srv", URL: codexServerURL, harness: harnessClaude})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := headers["Authorization"]; ok {
		t.Errorf("a Claude Code server was given codex's token: %v", headers)
	}
}

// codexHomeWithBothStores stages one session in the direct keyring and a different one in the
// fallback file, so the token alone says which store answered.
func codexHomeWithBothStores(t *testing.T, mode string) string {
	t.Helper()
	codexHome := t.TempDir()
	if mode != "" {
		writeFile(t, filepath.Join(codexHome, "config.toml"), "mcp_oauth_credentials_store = "+strconv.Quote(mode)+"\n")
	}
	fakeCodexKeyring(t, map[keyringEntry]string{
		{codexKeyringService, codexStoreKey("srv", codexServerURL)}: strings.Replace(
			codexRecord("srv", codexServerURL, "", 0), "stale-token", "keyring-token", 1),
	})
	codexFallbackFile(t, codexHome, map[string]string{
		"srv|abc": strings.Replace(codexFallbackEntry("srv", codexServerURL, "", 0), "stale-token", "file-token", 1),
	})
	return codexHome
}

// mcp_oauth_credentials_store decides which stores are consulted, as it does for codex: a user
// who moved to the file must not be authenticated with an obsolete keyring entry.
func TestCodexCredentialsStorePolicy(t *testing.T) {
	for _, test := range []struct{ name, mode, want string }{
		{"unset", "", "keyring-token"},
		{"auto", "auto", "keyring-token"},
		{"file", "file", "file-token"},
		{"keyring", "keyring", "keyring-token"},
		// A value from a newer codex is not a reason to refuse to run.
		{"unrecognized", "bogus", "keyring-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			token, err := codexOAuthToken(t.Context(), "srv", codexServerURL, codexHomeWithBothStores(t, test.mode))
			if err != nil || token != test.want {
				t.Fatalf("token = %q, err = %v, want %q", token, err, test.want)
			}
		})
	}
}

// keyring mode does not fall back to the file: codex fails instead of reading it, and using a
// session from a store the user excluded would authenticate with credentials codex would not.
func TestCodexKeyringStoreModeIgnoresTheFile(t *testing.T) {
	codexHome := t.TempDir()
	writeFile(t, filepath.Join(codexHome, "config.toml"), `mcp_oauth_credentials_store = "keyring"`)
	fakeCodexKeyring(t, nil)
	codexFallbackFile(t, codexHome, map[string]string{
		"srv|abc": codexFallbackEntry("srv", codexServerURL, "", 0),
	})

	token, err := codexOAuthToken(t.Context(), "srv", codexServerURL, codexHome)
	if err != nil || token != "" {
		t.Fatalf("token = %q, err = %v, want no session", token, err)
	}
}

func TestCodexRefreshReportsAStoreFailure(t *testing.T) {
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	codexHome := t.TempDir()
	account := codexStoreKey("srv", codexServerURL)
	fakeCodexKeyring(t, map[keyringEntry]string{
		{codexKeyringService, account}: codexRecord("srv", codexServerURL, authorization.url, expiredMillis()),
	})
	codexKeyringSet = func(context.Context, string, string, string) error {
		return errors.New("the keychain is locked")
	}

	_, err := codexOAuthToken(t.Context(), "srv", codexServerURL, codexHome)
	if err == nil || !strings.Contains(err.Error(), "could not store it") {
		t.Fatalf("got %v, want a store failure", err)
	}
}

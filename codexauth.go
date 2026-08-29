package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// codexTokens is codex's StoredOAuthTokens: the record the keyring and the secrets file hold.
// The token response stays raw so that a write-back can carry back every field of it,
// including the ones this program does not model.
type codexTokens struct {
	ServerName    string          `json:"server_name"`
	URL           string          `json:"url"`
	Issuer        string          `json:"issuer"`
	ClientID      string          `json:"client_id"`
	TokenResponse json.RawMessage `json:"token_response"`
	ExpiresAt     int64           `json:"expires_at"`
}

// codexTokenResponse is the part of the stored token response this program reads.
type codexTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// response returns the two fields of the token response this program uses. A response it
// cannot parse leaves them empty, which reads as no session rather than as a failure.
func (t codexTokens) response() codexTokenResponse {
	var response codexTokenResponse
	if err := json.Unmarshal(t.TokenResponse, &response); err != nil {
		return codexTokenResponse{}
	}
	return response
}

func (t codexTokens) accessToken() string  { return t.response().AccessToken }
func (t codexTokens) refreshToken() string { return t.response().RefreshToken }

// toOAuthEntry maps the record onto the shape the Claude Code path already refreshes, so
// refreshOAuthEntry is reused rather than written a second time.
func (t codexTokens) toOAuthEntry() oauthEntry {
	response := t.response()
	return oauthEntry{
		ServerName:   t.ServerName,
		ServerURL:    t.URL,
		AccessToken:  response.AccessToken,
		RefreshToken: response.RefreshToken,
		ExpiresAt:    t.ExpiresAt,
		ClientID:     t.ClientID,
		Issuer:       t.Issuer,
	}
}

// codexFileEntry is one entry of the plaintext fallback file, which stores the same session
// flattened instead of as a token response.
type codexFileEntry struct {
	ServerName    string `json:"server_name"`
	ServerURL     string `json:"server_url"`
	Issuer        string `json:"issuer"`
	ClientID      string `json:"client_id"`
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	ExpiresAt     int64  `json:"expires_at"`
	ExecutorOwned bool   `json:"executor_owned"`
}

func (e codexFileEntry) toOAuthEntry() oauthEntry {
	return oauthEntry{
		ServerName:   e.ServerName,
		ServerURL:    e.ServerURL,
		AccessToken:  e.AccessToken,
		RefreshToken: e.RefreshToken,
		ExpiresAt:    e.ExpiresAt,
		ClientID:     e.ClientID,
		Issuer:       e.Issuer,
	}
}

// matchesCodexServer reports whether a stored record belongs to the server being called: the URL
// compared exactly, the names compared with a local: prefix stripped from both. codex strips that
// prefix from the queried name only, so a record it filed under "local:x" matches a query for "x"
// here but not there. Only the name rule differs; the exact URL comparison is what keeps one
// server's token away from another.
func matchesCodexServer(storedName, storedURL, serverName, serverURL string) bool {
	return storedURL == serverURL &&
		strings.TrimPrefix(storedName, "local:") == strings.TrimPrefix(serverName, "local:")
}

// findCodexEntry returns the session stored for a server in the secrets file, along with the
// key it is filed under, so that a refresh is written back to that key. The key derived from
// the URL is tried first; the scan after it finds a session an older codex filed under a key
// of its own, the way findOAuthEntry matches on the fields rather than on the key.
func findCodexEntry(secrets map[string]string, serverName, serverURL string) (string, codexTokens, bool) {
	key := "global/" + codexSecretName(codexStoreKey(serverName, serverURL))
	if record, ok := secrets[key]; ok {
		var tokens codexTokens
		if err := json.Unmarshal([]byte(record), &tokens); err == nil {
			return key, tokens, true
		}
	}
	for key, record := range secrets {
		if !strings.HasPrefix(key, "global/MCP_OAUTH_") {
			continue
		}
		var tokens codexTokens
		if err := json.Unmarshal([]byte(record), &tokens); err != nil {
			continue
		}
		if matchesCodexServer(tokens.ServerName, tokens.URL, serverName, serverURL) {
			return key, tokens, true
		}
	}
	return "", codexTokens{}, false
}

// findCodexFileEntry is the same lookup in the fallback file. Entries the remote executor owns
// are skipped, as codex skips them for its own lookups: their tokens were minted for it.
func findCodexFileEntry(entries map[string]json.RawMessage, serverName, serverURL string) (string, codexFileEntry, bool) {
	for key, raw := range entries {
		var entry codexFileEntry
		if err := json.Unmarshal(raw, &entry); err != nil || entry.ExecutorOwned {
			continue
		}
		if matchesCodexServer(entry.ServerName, entry.ServerURL, serverName, serverURL) {
			return key, entry, true
		}
	}
	return "", codexFileEntry{}, false
}

// codexSession is a stored session together with the store it came from: a refresh goes back
// to that store alone, because that is the one codex will read it from again.
type codexSession struct {
	entry oauthEntry
	store func(ctx context.Context, token *oauth2.Token) error
}

// updateCodexTokens writes the refreshed tokens into a stored record, carrying every other
// field — including ones this program does not model — over verbatim, because dropping any of
// them would log the user out of that server. The two record shapes differ only in whether the
// tokens sit in a nested object, which is what nested names; expires_at is at the top of both.
// expires_in inside a token response is left alone: codex recomputes it from expires_at.
func updateCodexTokens(record []byte, nested string, token *oauth2.Token) ([]byte, error) {
	var fields map[string]any
	if err := json.Unmarshal(record, &fields); err != nil {
		return nil, err
	}
	tokens := fields
	if nested != "" {
		tokens, _ = fields[nested].(map[string]any)
		if tokens == nil {
			tokens = map[string]any{}
		}
		fields[nested] = tokens
	}
	tokens["access_token"] = token.AccessToken
	if token.RefreshToken != "" {
		tokens["refresh_token"] = token.RefreshToken
	}
	// A token endpoint may omit expires_in, which leaves the expiry zero; storing that would
	// claim the token expired in 1970 and refresh it again on the next call.
	if !token.Expiry.IsZero() {
		fields["expires_at"] = token.Expiry.UnixMilli()
	}
	return json.Marshal(fields)
}

// usableCodexToken returns the stored access token when it can be sent as it is. An absent
// expires_at means the token does not expire, as it does for codex's own token_needs_refresh;
// reading it as "expired in 1970" would spend a refresh token that may be single-use on every
// call. A session with no access token is nothing to refresh either, and reads as no session.
func usableCodexToken(session *codexSession) (string, bool) {
	entry := session.entry
	if entry.AccessToken == "" {
		return "", true
	}
	if entry.ExpiresAt == 0 || time.Now().Add(expiryMargin).Before(entry.expiry()) {
		return entry.AccessToken, true
	}
	return "", false
}

// codexOAuthToken returns an access token for the named server, refreshing the stored one when
// it has expired, or an empty string when codex holds no session for that server.
//
// The first read is unlocked so that the ordinary call — a token that is still valid — queues
// behind nothing. Only a refresh takes codex's per-credential lock, and it takes it before
// re-reading the session, because whoever held it before may already have refreshed: with a
// rotating refresh token, replaying the one this run first read would revoke the new one.
func codexOAuthToken(ctx context.Context, serverName, serverURL, codexHome string) (string, error) {
	session, err := findCodexSession(ctx, serverName, serverURL, codexHome)
	if err != nil || session == nil {
		return "", err
	}
	if token, usable := usableCodexToken(session); usable {
		return token, nil
	}
	var access string
	err = withCodexRefreshLock(codexHome, codexStoreKey(serverName, serverURL), func() error {
		session, err := findCodexSession(ctx, serverName, serverURL, codexHome)
		if err != nil || session == nil {
			return err
		}
		if token, usable := usableCodexToken(session); usable {
			access = token
			return nil
		}
		token, err := refreshOAuthEntry(ctx, session.entry)
		if err != nil {
			return err
		}
		if err := session.store(ctx, token); err != nil {
			return fmt.Errorf("refreshed the OAuth token for %q but could not store it: %w", serverName, err)
		}
		access = token.AccessToken
		return nil
	})
	if err != nil {
		return "", err
	}
	return access, nil
}

// findCodexSession searches the stores mcp_oauth_credentials_store allows, in the order codex's
// Auto mode consults them. A store that cannot be read is not proof that there is no session —
// codex falls back from an unavailable keyring to the file for exactly that case — so the
// search goes on and reports the failure only when nothing else holds one, rather than
// degrading a locked keychain into an unauthenticated call.
//
// ponytail: keyring mode reads both keyring-backed stores rather than resolving codex's
// secret_auth_storage feature flag to pick the direct entry or the encrypted secrets file.
// Reading the one the user does not use finds nothing, so the ceiling is a wasted lookup;
// resolve the flag if a session ever has to be told apart from the other backend's.
func findCodexSession(ctx context.Context, serverName, serverURL, codexHome string) (*codexSession, error) {
	loaders := []func() (*codexSession, error){
		func() (*codexSession, error) { return codexKeyringSession(ctx, serverName, serverURL) },
		func() (*codexSession, error) { return codexSecretsSession(ctx, serverName, serverURL, codexHome) },
		func() (*codexSession, error) { return codexFileSession(serverName, serverURL, codexHome) },
	}
	switch codexCredentialsStoreMode(codexHome) {
	case codexStoreFile:
		loaders = loaders[2:]
	case codexStoreKeyring:
		loaders = loaders[:2]
	}
	var failure error
	for _, load := range loaders {
		session, err := load()
		if err != nil {
			if failure == nil {
				failure = err
			}
			continue
		}
		if session != nil {
			return session, nil
		}
	}
	return nil, failure
}

// codexKeyringSession reads the entry codex keeps per session in the OS keyring.
func codexKeyringSession(ctx context.Context, serverName, serverURL string) (*codexSession, error) {
	account := codexStoreKey(serverName, serverURL)
	record, err := codexKeyringGet(ctx, codexKeyringService, account)
	if err != nil || record == "" {
		return nil, err
	}
	var tokens codexTokens
	// The record format is not a documented interface. A record this program cannot parse
	// reads as no session, and the call goes out unauthenticated for the server to answer.
	if err := json.Unmarshal([]byte(record), &tokens); err != nil {
		return nil, nil
	}
	return &codexSession{entry: tokens.toOAuthEntry(), store: func(ctx context.Context, token *oauth2.Token) error {
		// This entry holds this one session, so there is nothing else in it to lose by
		// updating the record just read, without the re-read the aggregate stores need.
		updated, err := updateCodexTokens([]byte(record), "token_response", token)
		if err != nil {
			return err
		}
		// ponytail: unverified against an entry codex wrote itself. codex writes these
		// through Security.framework, so updating one with `security add-generic-password -U`
		// may hit the macOS partition list and ask for the keychain password. If it does,
		// drop this write-back and return the refreshed token anyway.
		return codexKeyringSet(ctx, codexKeyringService, account, string(updated))
	}}, nil
}

// codexSecretsSession reads the age-encrypted secrets file, whose passphrase is the one thing
// codex keeps in the keyring when it stores sessions this way.
func codexSecretsSession(ctx context.Context, serverName, serverURL, codexHome string) (*codexSession, error) {
	passphrase, err := codexKeyringGet(ctx, codexSecretsService, codexPassphraseAccount(codexHome))
	if err != nil || passphrase == "" {
		return nil, err
	}
	var session *codexSession
	err = withCodexStoreLock(codexHome, codexSecretsLock, func() error {
		secrets, err := readCodexSecretsFile(codexHome, passphrase)
		if err != nil {
			return err
		}
		key, tokens, found := findCodexEntry(secrets, serverName, serverURL)
		if !found {
			return nil
		}
		session = &codexSession{entry: tokens.toOAuthEntry(), store: func(_ context.Context, token *oauth2.Token) error {
			return withCodexStoreLock(codexHome, codexSecretsLock, func() error {
				return storeRefreshedCodexSecret(codexHome, passphrase, key, token)
			})
		}}
		return nil
	})
	return session, err
}

// storeRefreshedCodexSecret writes the refreshed tokens into the secrets file. The file is read
// again under the lock rather than written back from the copy this run started with: the
// refresh took two HTTP round trips, and a codex session may have written another server's
// session in the meantime, which a whole-file write from the stale copy would undo.
//
// A key that is gone from the re-read is left gone: codex deletes an entry when the user logs
// out of that server, and writing it back would resurrect the session it just removed.
func storeRefreshedCodexSecret(codexHome, passphrase, key string, token *oauth2.Token) error {
	secrets, err := readCodexSecretsFile(codexHome, passphrase)
	if err != nil {
		return err
	}
	record, ok := secrets[key]
	if !ok {
		return nil
	}
	updated, err := updateCodexTokens([]byte(record), "token_response", token)
	if err != nil {
		return err
	}
	secrets[key] = string(updated)
	return writeCodexSecretsFile(codexHome, passphrase, secrets)
}

// codexFileSession reads the plaintext fallback file codex uses where no keyring is available.
func codexFileSession(serverName, serverURL, codexHome string) (*codexSession, error) {
	var session *codexSession
	err := withCodexStoreLock(codexHome, codexFileLock, func() error {
		entries, err := readCodexFallbackFile(codexHome)
		if err != nil {
			return err
		}
		key, entry, found := findCodexFileEntry(entries, serverName, serverURL)
		if !found {
			return nil
		}
		session = &codexSession{entry: entry.toOAuthEntry(), store: func(_ context.Context, token *oauth2.Token) error {
			return withCodexStoreLock(codexHome, codexFileLock, func() error {
				return storeRefreshedCodexEntry(codexHome, key, token)
			})
		}}
		return nil
	})
	return session, err
}

// storeRefreshedCodexEntry writes the refreshed tokens into the fallback file, re-reading it
// under the lock for the same reasons storeRefreshedCodexSecret does.
func storeRefreshedCodexEntry(codexHome, key string, token *oauth2.Token) error {
	entries, err := readCodexFallbackFile(codexHome)
	if err != nil {
		return err
	}
	entry, ok := entries[key]
	if !ok {
		return nil
	}
	updated, err := updateCodexTokens(entry, "", token)
	if err != nil {
		return err
	}
	entries[key] = updated
	return writeCodexFallbackFile(codexHome, entries)
}

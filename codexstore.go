package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/zalando/go-keyring"
)

const (
	// codexKeyringService holds one keyring entry per MCP session, keyed by the store key.
	codexKeyringService = "Codex MCP Credentials"
	// codexSecretsService holds the passphrase of the encrypted secrets file, keyed by home.
	codexSecretsService = "codex"
)

// The keyring accessors are indirected so that tests can replace them, the way
// keychainCredentials is on the Claude Code path: no test may read a real store, send a real
// token to a test server, or write to a real store.
var (
	codexKeyringGet = defaultCodexKeyringGet
	codexKeyringSet = defaultCodexKeyringSet
)

// defaultCodexKeyringGet returns the entry's value, or an empty string when there is none.
// macOS goes through the same `security` command Claude Code's credentials are read with, for
// the partition-list reason keychain.go explains; elsewhere go-keyring speaks to the Secret
// Service or the Credential Manager, which is what codex's own keyring crate does.
func defaultCodexKeyringGet(ctx context.Context, service, account string) (string, error) {
	if keychainAvailable {
		value, err := readKeychainAccount(ctx, service, account)
		if errors.Is(err, errKeychainNoEntry) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return string(value), nil
	}
	value, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return value, err
}

func defaultCodexKeyringSet(ctx context.Context, service, account, value string) error {
	if keychainAvailable {
		return writeKeychainAccount(ctx, service, account, []byte(value))
	}
	return keyring.Set(service, account, value)
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// jsonString encodes a string the way serde_json does. encoding/json escapes <, > and & for
// HTML by default, which serde does not, and a URL with a query string would then hash to
// something codex never wrote.
func jsonString(value string) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return `""`
	}
	return strings.TrimRight(encoded.String(), "\n")
}

// codexStoreKey is the account codex files a session under: the server name and the first 16
// hex digits of the hash of the connection. The hashed payload is written out by hand because
// its field order is part of the format — marshaling a Go map would sort the keys instead.
// codex's own server names carry a local: prefix that it strips before hashing.
func codexStoreKey(serverName, serverURL string) string {
	payload := `{"type":"http","url":` + jsonString(serverURL) + `,"headers":{}}`
	return strings.TrimPrefix(serverName, "local:") + "|" + sha256Hex(payload)[:16]
}

// codexSecretName is the store key hashed again, because the secrets file's key alphabet is
// only A-Z, 0-9 and _, which a server name and its punctuation do not fit into.
func codexSecretName(storeKey string) string {
	return "MCP_OAUTH_" + strings.ToUpper(sha256Hex(storeKey))[:32]
}

// codexPassphraseAccount is the keyring account holding the secrets file's passphrase. One
// passphrase belongs to one codex home, so the account names it by hash; codex canonicalizes
// the path first and falls back to the path as given when it cannot.
func codexPassphraseAccount(codexHome string) string {
	canonical := codexHome
	if absolute, err := filepath.Abs(canonical); err == nil {
		canonical = absolute
	}
	if resolved, err := filepath.EvalSymlinks(canonical); err == nil {
		canonical = resolved
	}
	return "secrets|" + sha256Hex(canonical)[:16]
}

// The two stores codex keeps more than one session in are guarded by an advisory lock, one
// file per store, in a directory of codex's own.
const (
	codexLockDir     = "mcp-oauth-locks"
	codexSecretsLock = "secrets-store.lock"
	codexFileLock    = "file-store.lock"
)

// withCodexStoreLock holds codex's lock for the store while fn works on it, so a read or a
// write does not interleave with codex's own read-modify-write of the same file.
//
// ponytail: the lock is taken exclusively and without a timeout, where codex shares it for
// reads and gives up after a minute. codex holds it for the file access alone, so the wait is
// short; a wedged holder would need the bounded acquire withCodexRefreshLock already does.
func withCodexStoreLock(codexHome, name string, fn func() error) error {
	// A codex home that does not exist has no store to lock, and creating one to hold a lock
	// file would leave a codex directory behind for a user who has no codex.
	if _, err := os.Stat(codexHome); err != nil {
		return fn()
	}
	dir := filepath.Join(codexHome, codexLockDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	// Closing the file releases the lock, so there is no unlock to get wrong.
	defer file.Close()
	if err := lockFile(file); err != nil {
		return err
	}
	return fn()
}

// codex's RefreshCredentialLock deadline and retry interval.
var (
	codexRefreshLockTimeout = 60 * time.Second
	codexRefreshLockRetry   = 50 * time.Millisecond
)

// withCodexRefreshLock holds codex's per-credential refresh lock for the whole refresh
// transaction — the authoritative re-read, the token endpoint round trip and the write-back —
// so two processes cannot both spend a rotating refresh token and log the user out of the
// server. It is a different file from the per-store locks, and it is always taken *outside*
// them: keeping that one order is what makes the nesting safe.
//
// Unlike those locks this one is bounded, because it is held across two HTTP round trips and a
// holder that never finishes would otherwise hang every later call forever. Off unix lockFile
// is a documented no-op and so is this: nothing is serialized there.
func withCodexRefreshLock(codexHome, storeKey string, fn func() error) error {
	// A codex home that does not exist has no credentials to serialize against, and creating
	// one to hold a lock file would leave a codex directory behind for a user who has no codex.
	if _, err := os.Stat(codexHome); err != nil {
		return fn()
	}
	dir := filepath.Join(codexHome, codexLockDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, sha256Hex(storeKey)+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	// Closing the file releases the lock, so there is no unlock to get wrong.
	defer file.Close()
	for deadline := time.Now().Add(codexRefreshLockTimeout); ; {
		locked, err := tryLockFile(file)
		if err != nil {
			return err
		}
		if locked {
			return fn()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for another process to finish refreshing the OAuth credentials locked by %s",
				codexRefreshLockTimeout, path)
		}
		time.Sleep(codexRefreshLockRetry)
	}
}

func codexSecretsPath(codexHome string) string {
	return filepath.Join(codexHome, "secrets", "mcp_oauth.age")
}

func codexFallbackPath(codexHome string) string {
	return filepath.Join(codexHome, ".credentials.json")
}

// readCodexSecretsFile decrypts the secrets file and returns its secrets by canonical key, or
// nil when there is no such file. A wrong passphrase or an unreadable file is an error: it
// means the session could not be read, not that there is none. A decrypted document this
// program cannot parse is treated as empty, the way an unparsable record is.
func readCodexSecretsFile(codexHome, passphrase string) (map[string]string, error) {
	ciphertext, err := os.ReadFile(codexSecretsPath(codexHome))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	decrypted, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, err
	}
	plaintext, err := io.ReadAll(decrypted)
	if err != nil {
		return nil, err
	}
	var file struct {
		Version int               `json:"version"`
		Secrets map[string]string `json:"secrets"`
	}
	// A document from a newer codex is one this program does not know how to read, so it
	// counts as no session rather than as records to be interpreted by the old rules.
	if err := json.Unmarshal(plaintext, &file); err != nil || file.Version > codexSecretsVersion {
		return nil, nil
	}
	return file.Secrets, nil
}

// codexSecretsVersion is the format version codex writes and refuses to read past.
const codexSecretsVersion = 1

func writeCodexSecretsFile(codexHome, passphrase string, secrets map[string]string) error {
	plaintext, err := json.Marshal(struct {
		Version int               `json:"version"`
		Secrets map[string]string `json:"secrets"`
	}{codexSecretsVersion, secrets})
	if err != nil {
		return err
	}
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return err
	}
	var ciphertext bytes.Buffer
	writer, err := age.Encrypt(&ciphertext, recipient)
	if err != nil {
		return err
	}
	if _, err := writer.Write(plaintext); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	path := codexSecretsPath(codexHome)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomically(path, ciphertext.Bytes())
}

// readCodexFallbackFile returns the entries of the plaintext store by store key, or nil when
// there is no such file. The entries stay raw so that a write-back keeps every field of the
// ones it does not touch.
func readCodexFallbackFile(codexHome string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(codexFallbackPath(codexHome))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, nil
	}
	return entries, nil
}

func writeCodexFallbackFile(codexHome string, entries map[string]json.RawMessage) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return writeFileAtomically(codexFallbackPath(codexHome), data)
}

// writeFileAtomically replaces a credential file through a temporary file in the same
// directory, so a crash mid-write leaves the old sessions in place rather than half a file.
func writeFileAtomically(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
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

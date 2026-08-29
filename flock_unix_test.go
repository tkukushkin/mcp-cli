//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortenRefreshLockTimeout keeps the tests that wait for a held lock from waiting the minute
// codex waits. Unix only, because the lock is a no-op where flock is.
func shortenRefreshLockTimeout(t *testing.T) {
	t.Helper()
	timeout := codexRefreshLockTimeout
	t.Cleanup(func() { codexRefreshLockTimeout = timeout })
	codexRefreshLockTimeout = 20 * time.Millisecond
}

// The lock file has to be the one codex takes, or the two programs serialize against nothing.
// The path is a golden value: printf 'srv|9a70b85417749d5c' | shasum -a 256
func TestCodexRefreshLockPathAndRelease(t *testing.T) {
	codexHome := t.TempDir()
	key := codexStoreKey("srv", codexServerURL)
	path := filepath.Join(codexHome, "mcp-oauth-locks",
		"848df9d2d3142b56362b53c576ad91dba5a24674c44b5617e439af8b4628325e.lock")

	if err := withCodexRefreshLock(codexHome, key, func() error {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("the lock is not the file codex locks: %v", err)
		}
		shortenRefreshLockTimeout(t)
		err := withCodexRefreshLock(codexHome, key, func() error {
			t.Error("the same credential lock was granted twice")
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Errorf("a contender got %v, want a timeout", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Leaving the transaction releases the lock rather than wedging every later run.
	if err := withCodexRefreshLock(codexHome, key, func() error { return nil }); err != nil {
		t.Fatalf("the lock outlived its transaction: %v", err)
	}
}

// The refresh transaction runs under that lock, so a process already refreshing this credential
// blocks this one instead of both spending the same rotating refresh token.
func TestCodexRefreshWaitsForTheCredentialLock(t *testing.T) {
	authorization := newAuthServer(t, "fresh-token", "rotated-refresh")
	codexHome := codexHomeWithFallback(t, map[string]string{
		"srv|abc": codexFallbackEntry("srv", codexServerURL, authorization.url, expiredMillis()),
	})

	shortenRefreshLockTimeout(t)
	err := withCodexRefreshLock(codexHome, codexStoreKey("srv", codexServerURL), func() error {
		_, err := codexOAuthToken(t.Context(), "srv", codexServerURL, codexHome)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v, want the refresh to wait for the lock", err)
	}
	if authorization.refreshSeen != "" {
		t.Error("the refresh token was spent while another holder had the lock")
	}
}

// A token that is still valid is served without touching the lock, so the common call does not
// queue behind anything.
func TestCodexValidTokenSkipsTheCredentialLock(t *testing.T) {
	codexHome := codexHomeWithFallback(t, map[string]string{
		"srv|abc": codexFallbackEntry("srv", codexServerURL, "", 0),
	})

	shortenRefreshLockTimeout(t)
	var token string
	if err := withCodexRefreshLock(codexHome, codexStoreKey("srv", codexServerURL), func() error {
		var err error
		token, err = codexOAuthToken(t.Context(), "srv", codexServerURL, codexHome)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if token != "stale-token" {
		t.Fatalf("token = %q, want the stored one", token)
	}
}

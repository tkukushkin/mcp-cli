package main

import (
	"strconv"
	"testing"
	"time"

	"github.com/keybase/go-keychain"
)

// ownKeychainEntry points the Keychain functions at a throwaway entry of this test's own, so
// that they run against the real Security.framework without ever touching the entry Claude
// Code keeps its credentials in.
func ownKeychainEntry(t *testing.T, data string) {
	t.Helper()
	service := "mcp-cli-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	item := keychain.NewItem()
	item.SetSecClass(keychain.SecClassGenericPassword)
	item.SetService(service)
	item.SetAccount("tester")
	item.SetData([]byte(data))
	item.SetAccessible(keychain.AccessibleWhenUnlocked)
	if err := keychain.AddItem(item); err != nil {
		t.Skipf("cannot use the Keychain here: %v", err)
	}

	original := keychainService
	keychainService = service
	t.Cleanup(func() {
		keychain.DeleteGenericPasswordItem(service, "tester")
		keychainService = original
	})
}

func TestReadKeychain(t *testing.T) {
	ownKeychainEntry(t, `{"mcpOAuth": {}}`)

	data, err := readKeychain()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"mcpOAuth": {}}` {
		t.Errorf("got %q", data)
	}
}

func TestWriteKeychainReplacesTheContents(t *testing.T) {
	ownKeychainEntry(t, `{"mcpOAuth": {}}`)

	if err := writeKeychain([]byte(`{"mcpOAuth": {"a": 1}}`)); err != nil {
		t.Fatal(err)
	}
	data, err := readKeychain()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"mcpOAuth": {"a": 1}}` {
		t.Errorf("got %q", data)
	}
}

// A second entry must not appear beside the first: Claude Code would keep reading the old one.
func TestWriteKeychainKeepsOneEntry(t *testing.T) {
	ownKeychainEntry(t, `{"mcpOAuth": {}}`)

	if err := writeKeychain([]byte(`{"updated": true}`)); err != nil {
		t.Fatal(err)
	}
	accounts, err := keychain.GetGenericPasswordAccounts(keychainService)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Errorf("got %d accounts, want 1", len(accounts))
	}
}

func TestKeychainWithoutAnEntry(t *testing.T) {
	original := keychainService
	keychainService = "mcp-cli-test-absent-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Cleanup(func() { keychainService = original })

	if _, err := readKeychain(); err == nil {
		t.Error("expected an error reading a missing entry")
	}
	if err := writeKeychain([]byte("{}")); err == nil {
		t.Error("expected an error writing a missing entry")
	}
}

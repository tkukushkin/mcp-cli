package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// securityStoreEnv points the stand-in `security` at the file backing it, and is what makes
// the test binary serve as that command instead of running the tests.
const securityStoreEnv = "MCP_CLI_TEST_SECURITY_STORE"

// installFakeSecurity copies the test binary to a directory as `security`, so that a copy of
// the tests themselves stands in for the command. It is a copy of this binary rather than a
// script because a script would need a shell, which Windows has none of.
func installFakeSecurity() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "mcp-cli-fake-security")
	if err != nil {
		return "", err
	}
	name := "security"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	fakeSecurityDir = dir
	return dir, copyFile(self, filepath.Join(dir, name))
}

var fakeSecurityDir string

func copyFile(from, to string) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		return err
	}
	return destination.Close()
}

// runFakeSecurity serves the three command forms this package uses, backed by a file instead
// of the Keychain. The real Keychain is never touched, so nothing asks for a password, while
// the command line this package builds and the output it parses are exercised end to end.
//
//	find-generic-password -s <service>              prints the attributes
//	find-generic-password -s <service> -w           prints the value
//	add-generic-password -U -A -s <service> -a <account> -w <value>
func runFakeSecurity(store string, args []string) int {
	appendLine(store+".log", strings.Join(args, " "))
	value, err := os.ReadFile(store)
	if err != nil {
		fmt.Fprintln(os.Stderr, "security: could not be found")
		return 44
	}
	switch {
	case len(args) == 0:
		fmt.Fprintln(os.Stderr, "unexpected: no arguments")
		return 1
	case args[0] == "find-generic-password" && args[len(args)-1] == "-w":
		os.Stdout.Write(value)
	case args[0] == "find-generic-password":
		account, _ := os.ReadFile(store + ".account")
		fmt.Printf("    \"acct\"<blob>=%s\n    \"svce\"<blob>=\"service\"\n", accountAttribute(string(account)))
	case args[0] == "add-generic-password":
		return writeFakeStore(store, args)
	default:
		fmt.Fprintln(os.Stderr, "unexpected:", strings.Join(args, " "))
		return 1
	}
	return 0
}

// accountAttribute prints an account the way `security` does: quoted, but as hex when it is
// not printable ASCII and as <NULL> when the entry has none.
func accountAttribute(account string) string {
	if strings.HasPrefix(account, "0x") || account == "<NULL>" {
		return account
	}
	return `"` + account + `"`
}

func writeFakeStore(store string, args []string) int {
	var account, value string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-a":
			account = args[i+1]
		case "-w":
			value = args[i+1]
		}
	}
	if err := os.WriteFile(store, []byte(value), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(store+".account", []byte(account), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func appendLine(path, line string) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	fmt.Fprintln(file, line)
}

// fakeSecurity backs the stand-in `security` with a store of this test's own and puts it
// first on PATH.
func fakeSecurity(t *testing.T, value string, exists bool) (store string, invocations func() []string) {
	t.Helper()
	dir := t.TempDir()
	store = filepath.Join(dir, "store")

	if exists {
		if err := os.WriteFile(store, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store+".account", []byte("tester"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(securityStoreEnv, store)
	t.Setenv("PATH", fakeSecurityDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return store, func() []string {
		recorded, err := os.ReadFile(store + ".log")
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimRight(string(recorded), "\n"), "\n")
	}
}

func TestReadKeychain(t *testing.T) {
	fakeSecurity(t, `{"mcpOAuth": {}}`, true)

	data, err := readKeychain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"mcpOAuth": {}}` {
		t.Errorf("got %q", data)
	}
}

// A value holding a newline or a tab comes back hex-encoded from `security`.
func TestReadKeychainDecodesHex(t *testing.T) {
	fakeSecurity(t, "7b2261223a20317d", true)

	data, err := readKeychain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"a": 1}` {
		t.Errorf("got %q", data)
	}
}

// JSON opens with a brace, so a real blob can never be mistaken for the hex form.
func TestReadKeychainLeavesJSONAlone(t *testing.T) {
	fakeSecurity(t, `{"beef": "cafe"}`, true)

	data, err := readKeychain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"beef": "cafe"}` {
		t.Errorf("got %q", data)
	}
}

func TestWriteKeychainReplacesTheContents(t *testing.T) {
	store, _ := fakeSecurity(t, `{"mcpOAuth": {}}`, true)

	if err := writeKeychain(t.Context(), []byte(`{"mcpOAuth": {"a": 1}}`)); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(store)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != `{"mcpOAuth": {"a": 1}}` {
		t.Errorf("got %q", written)
	}
}

// -U updates the entry in place instead of adding a second one, and -A is what leaves it
// open to every program. Losing either flag is what takes the entry away from Claude Code.
func TestWriteKeychainUpdatesInPlaceAndStaysOpen(t *testing.T) {
	_, invocations := fakeSecurity(t, "before", true)

	if err := writeKeychain(t.Context(), []byte("after")); err != nil {
		t.Fatal(err)
	}
	var add string
	for _, line := range invocations() {
		if strings.HasPrefix(line, "add-generic-password") {
			add = line
		}
	}
	if add == "" {
		t.Fatal("nothing was written")
	}
	for _, flag := range []string{"-U", "-A", "-a tester"} {
		if !strings.Contains(add, flag) {
			t.Errorf("%q is missing from %q", flag, add)
		}
	}
}

func TestKeychainWithoutAnEntry(t *testing.T) {
	fakeSecurity(t, "", false)

	_, err := readKeychain(t.Context())
	if err == nil {
		t.Error("expected an error reading a missing entry")
	}
	// A missing entry is the one failure that means "no credentials" rather than "could not
	// read them", and only it may degrade to an unauthenticated call.
	if !errors.Is(err, errKeychainNoEntry) {
		t.Errorf("got %v, want a missing-entry error", err)
	}
	if err := writeKeychain(t.Context(), []byte("{}")); err == nil {
		t.Error("expected an error writing a missing entry")
	}
}

// `security` prints an account that is not printable ASCII as hex instead of quoting it.
func TestKeychainAccountInHexForm(t *testing.T) {
	store, invocations := fakeSecurity(t, "before", true)
	if err := os.WriteFile(store+".account", []byte(`0x74657374`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeKeychain(t.Context(), []byte("after")); err != nil {
		t.Fatal(err)
	}
	var add string
	for _, line := range invocations() {
		if strings.HasPrefix(line, "add-generic-password") {
			add = line
		}
	}
	if !strings.Contains(add, "-a test") {
		t.Errorf("the hex account was not decoded: %q", add)
	}
}

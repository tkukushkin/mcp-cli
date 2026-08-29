package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSecurity puts a stand-in for the `security` command first on PATH, backed by a file
// instead of the Keychain. The real Keychain is never touched, so nothing asks for a
// password, while the command line this package builds and the output it parses are still
// exercised end to end.
//
// It understands the three forms this package uses:
//
//	find-generic-password -s <service>              prints the attributes
//	find-generic-password -s <service> -w           prints the value
//	add-generic-password -U -A -s <service> -a <account> -w <value>
func fakeSecurity(t *testing.T, value string, exists bool) (store string, invocations func() []string) {
	t.Helper()
	dir := t.TempDir()
	store = filepath.Join(dir, "store")
	log := filepath.Join(dir, "log")

	if exists {
		if err := os.WriteFile(store, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store+".account", []byte("tester"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	script := `#!/bin/bash
store=` + store + `
log=` + log + `
printf '%s\n' "$*" >> "$log"

case "$1" in
find-generic-password)
  [ -f "$store" ] || { echo "security: could not be found" >&2; exit 44; }
  if [ "${*: -1}" = "-w" ]; then
    cat "$store"
  else
    printf '    "acct"<blob>="%s"\n    "svce"<blob>="service"\n' "$(cat "$store".account)"
  fi
  ;;
add-generic-password)
  [ -f "$store" ] || { echo "security: could not be found" >&2; exit 44; }
  account=""
  value=""
  while [ $# -gt 0 ]; do
    case "$1" in
      -a) account="$2"; shift 2;;
      -w) value="$2"; shift 2;;
      *) shift;;
    esac
  done
  printf '%s' "$value" > "$store"
  printf '%s' "$account" > "$store".account
  ;;
*)
  echo "unexpected: $*" >&2; exit 1;;
esac
`
	path := filepath.Join(dir, "security")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return store, func() []string {
		recorded, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimRight(string(recorded), "\n"), "\n")
	}
}

func TestReadKeychain(t *testing.T) {
	fakeSecurity(t, `{"mcpOAuth": {}}`, true)

	data, err := readKeychain()
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

	data, err := readKeychain()
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

	data, err := readKeychain()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"beef": "cafe"}` {
		t.Errorf("got %q", data)
	}
}

func TestWriteKeychainReplacesTheContents(t *testing.T) {
	store, _ := fakeSecurity(t, `{"mcpOAuth": {}}`, true)

	if err := writeKeychain([]byte(`{"mcpOAuth": {"a": 1}}`)); err != nil {
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

	if err := writeKeychain([]byte("after")); err != nil {
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

	if _, err := readKeychain(); err == nil {
		t.Error("expected an error reading a missing entry")
	}
	if err := writeKeychain([]byte("{}")); err == nil {
		t.Error("expected an error writing a missing entry")
	}
}

package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// keychainAvailable reports whether Claude Code keeps its credentials in the Keychain here,
// as it does on macOS, rather than in a file. It is a variable so that tests can exercise
// both stores on whichever platform they run.
var keychainAvailable = runtime.GOOS == "darwin"

// The Keychain is reached through the `security` command rather than Security.framework,
// which matters more than it looks.
//
// A Keychain entry has two gates: its access control list, and a partition list that macOS
// assigns to whoever writes the entry. The second one decides, and changing it needs the
// keychain password. So whichever program writes the entry takes it over, and the other one
// starts being asked for a password — which is exactly what happens in both directions
// between Claude Code and a program of our own.
//
// Claude Code goes through `security`, so the entry belongs to `security`. Going through the
// same command keeps both programs on the same side of that gate instead of taking the entry
// away from the harness on every refresh.
//
// The cost is that writing passes the credentials as a command-line argument, where `ps` can
// see them for the moment the command runs; `security` offers no way to feed a password in
// on standard input. Claude Code writes them the same way.

// errKeychainNoEntry is what `security` reports as exit code 44. It is the one failure that
// means there are no credentials rather than that they could not be read.
var errKeychainNoEntry = errors.New("no Keychain entry")

func keychainFailure(err error, service string) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 44 {
		return fmt.Errorf("%w for %q", errKeychainNoEntry, service)
	}
	return err
}

// An account is printed quoted, hex-encoded when it is not printable ASCII, or as <NULL> when
// the entry has none.
var keychainAccountPattern = regexp.MustCompile(`"acct"<blob>=(?:0x([0-9A-Fa-f]+)|"([^"]*)"|<NULL>)`)

// keychainAccount reports which account the entry is filed under, so an update replaces it
// instead of adding a second entry beside it.
func keychainAccount(ctx context.Context, service string) (string, error) {
	out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", service).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("no %q Keychain entry: %w", service, keychainFailure(err, service))
	}
	match := keychainAccountPattern.FindSubmatch(out)
	if match == nil {
		return "", fmt.Errorf("cannot read the account of the %q Keychain entry", service)
	}
	if len(match[1]) > 0 {
		decoded, err := hex.DecodeString(string(match[1]))
		if err != nil {
			return "", fmt.Errorf("cannot decode the account of the %q Keychain entry: %w", service, err)
		}
		return string(decoded), nil
	}
	return string(match[2]), nil
}

// hexOnly matches the form `security` falls back to when the stored value is not printable.
var hexOnly = regexp.MustCompile(`^(?:[0-9a-f]{2})+$`)

// findGenericPassword is what readKeychain and readKeychainAccount both boil down to: the
// same lookup and hex-decoding, with the account omitted or pinned depending on whether the
// caller already knows which entry it wants.
func findGenericPassword(ctx context.Context, service, account string) ([]byte, error) {
	args := []string{"find-generic-password", "-s", service}
	if account != "" {
		args = append(args, "-a", account)
	}
	args = append(args, "-w")
	out, err := exec.CommandContext(ctx, "security", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("cannot read the %q Keychain entry: %w", service, keychainFailure(err, service))
	}
	value := strings.TrimRight(string(out), "\n")

	// A value holding a newline or a tab comes back hex-encoded instead of verbatim. A
	// credential blob is JSON, so it opens with a brace and can never be mistaken for hex.
	if hexOnly.MatchString(value) {
		decoded, err := hex.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("cannot decode the %q Keychain entry: %w", service, err)
		}
		return decoded, nil
	}
	return []byte(value), nil
}

func readKeychain(ctx context.Context, service string) ([]byte, error) {
	return findGenericPassword(ctx, service, "")
}

// readKeychainAccount reads the entry filed under a specific account, for a caller that
// already knows the account (a store key from a previous write) and must not go through
// keychainAccount's discover-the-account lookup, which would find the wrong entry once a
// service holds more than one account.
func readKeychainAccount(ctx context.Context, service, account string) ([]byte, error) {
	return findGenericPassword(ctx, service, account)
}

// writeKeychainAccount replaces the contents of the entry filed under a known account,
// keeping it open to every program. The -U flag updates the entry in place and -A is what
// leaves the access control open.
func writeKeychainAccount(ctx context.Context, service, account string, contents []byte) error {
	cmd := exec.CommandContext(ctx, "security", "add-generic-password",
		"-U", "-A", "-s", service, "-a", account, "-w", string(contents))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cannot update the %q Keychain entry: %w: %s", service, err, out)
	}
	return nil
}

// writeKeychain replaces the entry's contents, discovering which account it is filed under
// first. A caller that already knows the account should use writeKeychainAccount instead, so
// that it writes to the entry it means rather than whichever one keychainAccount finds first.
func writeKeychain(ctx context.Context, service string, contents []byte) error {
	account, err := keychainAccount(ctx, service)
	if err != nil {
		return err
	}
	return writeKeychainAccount(ctx, service, account, contents)
}

//go:build !darwin

package main

import "errors"

// keychainAvailable reports that this platform keeps Claude Code's credentials in a file,
// so the Keychain functions below are never reached.
const keychainAvailable = false

var errNoKeychain = errors.New("no Keychain on this platform")

func readKeychain() ([]byte, error) { return nil, errNoKeychain }

func writeKeychain([]byte) error { return errNoKeychain }

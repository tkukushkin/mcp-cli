package main

import (
	"fmt"

	"github.com/keybase/go-keychain"
)

// keychainAvailable reports that this platform keeps Claude Code's credentials in the
// Keychain rather than in a file.
const keychainAvailable = true

// keychainEntryAccount reports which account the entry is filed under, so that an update
// replaces it instead of adding a second entry beside it.
func keychainEntryAccount() (string, error) {
	accounts, err := keychain.GetGenericPasswordAccounts(keychainService)
	if err != nil {
		return "", fmt.Errorf("cannot read the %q Keychain entry: %w", keychainService, err)
	}
	if len(accounts) == 0 {
		return "", fmt.Errorf("no %q Keychain entry", keychainService)
	}
	return accounts[0], nil
}

func readKeychain() ([]byte, error) {
	account, err := keychainEntryAccount()
	if err != nil {
		return nil, err
	}
	return keychain.GetGenericPassword(keychainService, account, "", "")
}

// writeKeychain replaces the entry's contents. It goes through Security.framework rather
// than the `security` command so that the credentials never appear in an argv, where any
// process could read them out of `ps` for the duration of the call.
func writeKeychain(data []byte) error {
	account, err := keychainEntryAccount()
	if err != nil {
		return err
	}
	query := keychain.NewItem()
	query.SetSecClass(keychain.SecClassGenericPassword)
	query.SetService(keychainService)
	query.SetAccount(account)

	update := keychain.NewItem()
	update.SetData(data)

	if err := keychain.UpdateItem(query, update); err != nil {
		return fmt.Errorf("cannot update the %q Keychain entry: %w", keychainService, err)
	}
	return nil
}

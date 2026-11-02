package main

import (
	"errors"
	"os"
	"path/filepath"
)

// apiTokenEnv names the variable that carries an admin API token. The
// console's token page prints the same name.
const apiTokenEnv = "STRAZA_API_TOKEN" // #nosec G101 -- the variable's name, not a credential

// errNoSignIn answers the retired login and logout while a token is set.
var errNoSignIn = errors.New("strazactl no longer signs in. It runs on the admin API token in STRAZA_API_TOKEN, " +
	"so there is nothing to sign in or out of")

// oldLoginPath is where strazactl kept its login before it ran on admin API
// tokens alone: ~/.straza/credentials.json. strazactl only names the file,
// so its owner can delete it.
func oldLoginPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".straza", "credentials.json")
}

// noTokenError is the refusal of a command that needs an admin API token
// when STRAZA_API_TOKEN holds none. When the old login file at loginPath is
// still there, it adds that the file works nowhere and can be deleted.
func noTokenError(loginPath string) error {
	msg := "strazactl needs an admin API token in STRAZA_API_TOKEN. A person mints one in the console under Settings, API tokens, " +
		"with the scope these commands need and an expiry"
	if _, err := os.Stat(loginPath); err == nil {
		msg += ". strazactl no longer signs in, so the login kept in " + loginPath + " works nowhere and can be deleted"
	}
	return errors.New(msg)
}

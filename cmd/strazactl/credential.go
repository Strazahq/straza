package main

import (
	"errors"
	"fmt"
)

// apiTokenEnv names the variable that carries an admin API token. The
// console's token page prints the same name.
const apiTokenEnv = "STRAZA_API_TOKEN" // #nosec G101 -- the variable's name, not a credential

// errLoginWithToken refuses login while a token is set: every admin call
// would send the token, so the login made here would never be used.
var errLoginWithToken = errors.New("STRAZA_API_TOKEN is set, so strazactl sends that admin API token on every admin call " +
	"and would never use a login made here. Unset STRAZA_API_TOKEN, then run strazactl login again")

// errTokenWithoutServer replaces ErrNoServer while a token is set, because
// its advice, to log in, is refused while the token is set.
var errTokenWithoutServer = errors.New("STRAZA_API_TOKEN is set, but no server is given. " +
	"Pass --server <url> or set STRAZA_SERVER to the server that issued the token")

// loginNote is the line a successful login prints on stderr.
const loginNote = "note: this login can change Straza's configuration. Keep it away from coding agents. " +
	"Automation uses an admin API token in STRAZA_API_TOKEN, and an agent proposes config changes through the built-in straza MCP server."

// logoutTokenNote is the line logout prints while a token is set, because
// ending the login leaves the token working in this shell.
const logoutTokenNote = "note: STRAZA_API_TOKEN is still set, so strazactl keeps using that admin API token until you unset it." // #nosec G101 -- an operator sentence, not a credential

// tokenNotice is the one stderr line a command prints when its admin calls
// send the token in place of the login, naming the server they go to.
func tokenNotice(server string) string {
	return fmt.Sprintf("note: strazactl uses the admin API token in STRAZA_API_TOKEN for %s, not a strazactl login.", server)
}

package ctl

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// ErrNoServer is what a server-touching strazactl command fails with when
// nothing says which strazad to talk to: no --server, no $STRAZA_SERVER, and
// no stored login. There is deliberately no localhost default: a silent one
// would let a stale login aim admin commands at whatever happens to listen on
// the loopback port, as whatever identity the old credentials carried.
var ErrNoServer = errors.New("not logged in and no server given. Run `strazactl login --server <url>`")

// Source names where a resolved server came from, spelled the way the operator
// wrote it so a warning can point at the exact thing to change.
type Source string

const (
	// SourceFlag is the --server flag.
	SourceFlag Source = "--server"
	// SourceEnv is the $STRAZA_SERVER environment variable.
	SourceEnv Source = "$STRAZA_SERVER"
	// SourceCredentials is the server `strazactl login` recorded (the default).
	SourceCredentials Source = "your login"
)

// Target is the strazad endpoint one strazactl invocation resolved, carrying
// enough origin detail for the caller to warn when an override disagrees with the
// login the credentials belong to.
type Target struct {
	// Server is the base URL to talk to, normalized (no trailing slash).
	Server string
	// Source is where Server came from.
	Source Source
	// LoginServer is the server the stored credentials belong to, empty when
	// there is no readable login on this machine.
	LoginServer string
}

// Overridden reports whether --server or $STRAZA_SERVER aimed this invocation
// at a different deployment than the stored credentials came from, the case
// where the session token in hand is probably worthless at the target.
func (t Target) Overridden() bool {
	return t.Source != SourceCredentials && t.LoginServer != "" && t.LoginServer != t.Server
}

// ResolveTarget applies strazactl's server resolution order: the --server flag
// beats $STRAZA_SERVER, which beats the server `strazactl login` stored, and
// with none of the three it returns ErrNoServer rather than guessing.
//
// credsPath is the credentials file to read the stored login from
// (DefaultCredsPath in production). A missing, unreadable or corrupt file just
// means "not logged in": it contributes a default and never fails a call that
// supplied a server itself.
func ResolveTarget(flagValue, envValue, credsPath string) (Target, error) {
	t := Target{LoginServer: storedServer(credsPath)}
	switch {
	case normalizeServer(flagValue) != "":
		t.Server, t.Source = normalizeServer(flagValue), SourceFlag
	case normalizeServer(envValue) != "":
		t.Server, t.Source = normalizeServer(envValue), SourceEnv
	case t.LoginServer != "":
		t.Server, t.Source = t.LoginServer, SourceCredentials
	default:
		return Target{}, ErrNoServer
	}
	return t, nil
}

// storedServer returns the normalized server `strazactl login` recorded, or ""
// when this machine has no readable login. Resolution reports the actionable
// ErrNoServer rather than a JSON parse error nobody asked about.
func storedServer(credsPath string) string {
	raw, err := os.ReadFile(credsPath) // #nosec G304 -- strazactl's own credentials path
	if err != nil {
		return ""
	}
	var creds credentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return ""
	}
	return normalizeServer(creds.Server)
}

// normalizeServer trims surrounding space and trailing slashes so that
// "https://x/" and "https://x" are the same deployment for comparison.
func normalizeServer(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), "/")
}

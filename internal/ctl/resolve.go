package ctl

import (
	"errors"
	"strings"
)

// ErrNoServer is what a server-touching strazactl command fails with when
// neither --server nor $STRAZA_SERVER says which strazad to talk to. There is
// deliberately no localhost default: a silent one would aim admin commands,
// and the token they carry, at whatever happens to listen on the loopback
// port.
var ErrNoServer = errors.New("no server is given. Pass --server <url> or set STRAZA_SERVER to the server that issued the token")

// ResolveServer applies strazactl's server resolution order: the --server
// flag beats $STRAZA_SERVER, and with neither it returns ErrNoServer rather
// than guessing. The answer is normalized, with no trailing slash.
func ResolveServer(flagValue, envValue string) (string, error) {
	switch {
	case normalizeServer(flagValue) != "":
		return normalizeServer(flagValue), nil
	case normalizeServer(envValue) != "":
		return normalizeServer(envValue), nil
	}
	return "", ErrNoServer
}

// normalizeServer trims surrounding space and trailing slashes so that
// "https://x/" and "https://x" are the same deployment.
func normalizeServer(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), "/")
}

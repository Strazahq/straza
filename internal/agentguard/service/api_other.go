//go:build !linux && !darwin && !windows

package service

import (
	"errors"
	"net"
)

// errNotOnLinux is what `straza service` answers on every system other
// than Linux, macOS and Windows.
var errNotOnLinux = errors.New("`straza service` runs only on Linux, macOS and Windows in this version of Straza, " + //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
	"so this machine cannot hold Straza sessions yet.")

// supported refuses every system other than Linux, macOS and Windows.
func supported() error { return errNotOnLinux }

// listen is never reached off Linux, macOS and Windows, because supported
// refuses first.
func listen(Options) (net.Listener, error) { return nil, errNotOnLinux }

// peerOf cannot name a caller off Linux, macOS and Windows.
func peerOf(net.Conn) (Caller, error) { return Caller{}, errNotOnLinux }

// ownUID names no account off Linux, macOS and Windows.
func ownUID() string { return "" }

// isUID takes no folder name for a user's off Linux, macOS and Windows.
func isUID(string) bool { return false }

//go:build unix

package localapi

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

func (c *Client) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	if err := checkOwner(c.socket, c.user); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	return conn, nil
}

// lookupAccount looks up an OS account by name in the system's account
// database: a pure Go build reads /etc/passwd on Linux and asks Directory
// Services through libSystem on macOS. Tests replace it, because the
// service's account exists only where Straza is installed.
var lookupAccount = user.Lookup

// checkOwner refuses a socket path that is not a socket, or whose owner
// ownerRefusal refuses.
func checkOwner(path string, userMode bool) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode().Type() != os.ModeSocket {
		return fmt.Errorf("%s is not a socket", path)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read the owner of %s", path)
	}
	return ownerRefusal(path, int(st.Uid), userMode)
}

// ownerRefusal refuses a socket at path that owner owns unless owner is
// root or the service's account, or in user mode unless owner is the
// caller.
func ownerRefusal(path string, owner int, userMode bool) error {
	if userMode {
		if owner != os.Getuid() {
			return fmt.Errorf("%s belongs to uid %d, not to this user", path, owner)
		}
		return nil
	}
	if owner == 0 {
		return nil
	}
	if u, err := lookupAccount(ServiceAccount); err == nil && u.Uid == strconv.Itoa(owner) {
		return nil
	}
	return fmt.Errorf("%s belongs to uid %d, which is not the %s account", path, owner, ServiceAccount)
}

// SystemPresent reports whether this machine has a system service: the
// directory of SystemSocket exists and belongs to root or the service's
// account. That is /run/straza-service on Linux, which systemd makes, and
// /opt/straza/run on macOS, which the Straza package makes at install
// time, so it holds on a Mac even while the service is stopped. On such a
// machine the thin clients ignore STRAZA_HOME, so the environment of a
// harness cannot point a hook at a socket the caller owns.
func SystemPresent() bool {
	fi, err := os.Lstat(filepath.Dir(SystemSocket))
	if err != nil || !fi.IsDir() {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	if st.Uid == 0 {
		return true
	}
	u, err := lookupAccount(ServiceAccount)
	return err == nil && u.Uid == strconv.FormatUint(uint64(st.Uid), 10)
}

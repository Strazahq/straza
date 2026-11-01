//go:build linux || darwin

package service

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// supported reports that the service runs on this system.
func supported() error { return nil }

// listenSocket listens on path. In system mode every uid may connect and
// the service checks each caller, so the socket is 0666. In user mode only
// the service's own user may connect, so it is 0600 in a 0700 directory.
func listenSocket(path string, userMode bool) (net.Listener, error) {
	dirMode, sockMode := fs.FileMode(0o755), fs.FileMode(0o666)
	if userMode {
		dirMode, sockMode = 0o700, 0o600
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return nil, fmt.Errorf("`straza service` could not create the directory of its socket %s (%w). "+
			"Check that this user may write there", path, err)
	}
	if err := removeStale(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("`straza service` could not listen on %s (%w). Check that this user may write there", path, err)
	}
	if err := os.Chmod(path, sockMode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("`straza service` could not set the mode of its socket %s (%w)", path, err)
	}
	return ln, nil
}

// removeStale removes a socket file at path that nothing answers on, which
// a service that died leaves behind. A live socket or a file of another
// kind stays and stops this service.
func removeStale(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("`straza service` could not check its socket path %s (%w)", path, err)
	case fi.Mode().Type() != fs.ModeSocket:
		return fmt.Errorf("%s exists and is not a socket, so `straza service` will not replace it. "+
			"Move that file away, then start the service again", path)
	}
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = conn.Close()
		return fmt.Errorf("another Straza background service answers on %s, so this one stopped. "+
			"Stop the other one first, or set a different STRAZA_HOME", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("`straza service` could not remove the stale socket %s (%w)", path, err)
	}
	return nil
}

// ownUID is the uid the service runs as, spelled as a Caller's UID.
func ownUID() string { return strconv.Itoa(os.Getuid()) }

// isUID reports whether name is a uid as a Caller spells it, so a folder in
// the state directory with any other name is never taken for a user's.
func isUID(name string) bool {
	v, err := strconv.ParseUint(name, 10, 32)
	return err == nil && strconv.FormatUint(v, 10) == name
}

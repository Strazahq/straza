//go:build darwin

package service

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// errRunDir stops a system service whose socket folder is not the one the
// Straza package made.
var errRunDir = errors.New("`straza service` cannot use /opt/straza/run, because it is missing, does not belong to _straza-service, or others may change it. " + //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
	"Install the Straza package again, which creates it, then start the service with `sudo launchctl kickstart system/ai.straza.service`.")

// listen returns a socket of the service's own at opts.Socket. The system
// service runs as its own account, which cannot make a folder under /opt,
// so it serves only in the folder the pkg made, and listenSocket then
// creates nothing above the socket. A user-mode service makes its own
// folder, as on Linux.
func listen(opts Options) (net.Listener, error) {
	if !opts.UserMode {
		if err := checkRunDir(filepath.Dir(opts.Socket)); err != nil {
			return nil, err
		}
	}
	return listenSocket(opts.Socket, opts.UserMode)
}

// checkRunDir refuses dir unless it is a folder, not a link, that the
// service's own uid owns and that neither group nor others may write, so
// nobody else can swap the socket the hooks dial.
func checkRunDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm()&0o022 != 0 {
		return errRunDir
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return errRunDir
	}
	return nil
}

// peerOf reads the uid of conn's other end from the kernel with
// LOCAL_PEERCRED, which holds the effective uid the peer connected with.
// LOCAL_PEERPID names the last process that used the socket, so its pid is
// information only, and a pid that cannot be read stays 0.
func peerOf(conn net.Conn) (Caller, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Caller{}, fmt.Errorf("the connection is %T, not a Unix socket", conn)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Caller{}, err
	}
	var cred *unix.Xucred
	var credErr error
	pid := 0
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)        // #nosec G115 -- a file descriptor fits an int
		if p, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); err == nil { // #nosec G115 -- a file descriptor fits an int
			pid = p
		}
	}); err != nil {
		return Caller{}, err
	}
	if credErr != nil {
		return Caller{}, credErr
	}
	return callerFromXucred(cred, pid)
}

// callerFromXucred names the caller cred describes. It refuses a version
// other than 0, the only layout macOS defines (XUCRED_VERSION), because the
// uid of another layout may sit elsewhere.
func callerFromXucred(cred *unix.Xucred, pid int) (Caller, error) {
	if cred.Version != 0 {
		return Caller{}, fmt.Errorf("the kernel's peer credentials are version %d, and Straza reads only version 0", cred.Version)
	}
	return Caller{UID: strconv.FormatUint(uint64(cred.Uid), 10), PID: int32(pid)}, nil // #nosec G115 -- a pid fits an int32 on macOS
}

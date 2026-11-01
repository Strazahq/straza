//go:build linux

package service

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
)

// listen returns the one socket systemd passed, or else a socket of the
// service's own at opts.Socket.
func listen(opts Options) (net.Listener, error) {
	ln, err := systemdListener()
	if ln != nil || err != nil {
		return ln, err
	}
	return listenSocket(opts.Socket, opts.UserMode)
}

// systemdListener returns the socket systemd passed on fd 3, or nil when
// LISTEN_PID does not name this process. It unsets the activation
// variables, so no process the service starts takes them for its own.
func systemdListener() (net.Listener, error) {
	pid, err := strconv.Atoi(os.Getenv("LISTEN_PID"))
	if err != nil || pid != os.Getpid() {
		return nil, nil
	}
	fds := os.Getenv("LISTEN_FDS")
	for _, k := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		_ = os.Unsetenv(k)
	}
	if fds != "1" {
		return nil, fmt.Errorf("systemd passed %q sockets to `straza service`, which serves exactly one. "+
			"Give straza-service.socket a single ListenStream line, then restart straza-service.socket", fds)
	}
	syscall.CloseOnExec(3)
	f := os.NewFile(3, "straza-service.socket")
	defer func() { _ = f.Close() }()
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("the socket systemd passed to `straza service` is not one it can serve (%w). "+
			"Check that straza-service.socket listens on a Unix stream socket", err)
	}
	return ln, nil
}

// peerOf reads the uid and pid of conn's other end from the kernel with
// SO_PEERCRED.
func peerOf(conn net.Conn) (Caller, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Caller{}, fmt.Errorf("the connection is %T, not a Unix socket", conn)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Caller{}, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED) // #nosec G115 -- a file descriptor fits an int
	}); err != nil {
		return Caller{}, err
	}
	if credErr != nil {
		return Caller{}, credErr
	}
	return Caller{UID: strconv.FormatUint(uint64(cred.Uid), 10), PID: cred.Pid}, nil
}

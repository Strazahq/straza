//go:build windows

package service

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// supported reports that the service runs on this system.
func supported() error { return nil }

// listen creates the service's pipe at opts.Socket with pipeSDDL. The
// listener holds the pipe's name until it closes, and creation fails when
// the name already exists, so the service never joins a pipe whose access
// rules someone else set.
func listen(opts Options) (net.Listener, error) {
	own, err := tokenUser()
	if err != nil {
		return nil, fmt.Errorf("`straza service` could not read the Windows account it runs as (%w), so it did not "+
			"create its pipe %s. Check the account of the service with `sc.exe qc straza-service`", err, opts.Socket)
	}
	ln, err := winio.ListenPipe(opts.Socket, &winio.PipeConfig{SecurityDescriptor: pipeSDDL(own)})
	switch {
	case err == nil:
		return ln, nil
	case errors.Is(err, windows.ERROR_ACCESS_DENIED), errors.Is(err, windows.ERROR_PIPE_BUSY),
		errors.Is(err, windows.ERROR_ALREADY_EXISTS):
		return nil, fmt.Errorf("`straza service` could not create its pipe %s, because another program already holds "+
			"that name (%w). Restart the computer, or ask an administrator to find and stop that program, then start "+
			"the service again", opts.Socket, err)
	}
	return nil, fmt.Errorf("`straza service` could not create its pipe %s (%w). Start the service again, and if this "+
		"keeps happening, tell your Straza admin", opts.Socket, err)
}

// pipeSDDL is the security descriptor of the pipe of a service running as
// own. own owns the pipe, which is what a thin client checks before it
// writes. SYSTEM and own get full access, which each further instance of
// the pipe needs. Every signed-in account gets localapi.PipeAccess, which
// reads and writes but never creates an instance, and the service then
// names each caller itself. The DACL is protected, so nothing inherits
// into it.
func pipeSDDL(own string) string {
	return fmt.Sprintf("O:%sD:P(A;;FA;;;SY)(A;;FA;;;%s)(A;;0x%x;;;AU)", own, own, localapi.PipeAccess)
}

// impersonateNamedPipeClient lets a pipe server's thread act as the pipe's
// client. golang.org/x/sys does not declare it.
var impersonateNamedPipeClient = windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient")

// peerOf names conn's other end: the account Windows names for the pipe's
// client and the client's process id. Windows names the client of the last
// message the service read, so it runs only after a request was read. Any
// failed call, and a client that dialed anonymously, is an error.
func peerOf(conn net.Conn) (Caller, error) {
	f, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return Caller{}, fmt.Errorf("the connection is %T, not a named pipe", conn)
	}
	h := windows.Handle(f.Fd())
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(h, &pid); err != nil {
		return Caller{}, fmt.Errorf("read the pipe client's process id: %w", err)
	}
	sid, err := clientAccount(h)
	if err != nil {
		return Caller{}, err
	}
	return Caller{UID: sid, PID: int32(pid)}, nil // #nosec G115 -- a Windows process id fits an int32
}

// clientAccount returns the SID of the account of the client of the pipe
// h, read by threadAccount while the thread acts as the client.
func clientAccount(h windows.Handle) (string, error) { return asPipeClient(h, threadAccount) }

// asPipeClient acts as the client of the pipe h on a goroutine of its own,
// locked to its OS thread, runs read there and reverts before it unlocks.
// When the revert fails, the goroutine ends still locked, so the Go runtime
// ends that thread and no other code ever runs as the client.
func asPipeClient(h windows.Handle, read func() (string, error)) (string, error) {
	if err := impersonateNamedPipeClient.Find(); err != nil {
		return "", err
	}
	type answer struct {
		sid string
		err error
	}
	done := make(chan answer, 1)
	go func() {
		runtime.LockOSThread()
		var a answer
		if r, _, err := impersonateNamedPipeClient.Call(uintptr(h)); r == 0 {
			a.err = fmt.Errorf("act as the pipe client: %w", err)
		} else {
			a.sid, a.err = read()
		}
		if err := windows.RevertToSelf(); err != nil {
			done <- answer{err: fmt.Errorf("stop acting as the pipe client: %w", err)}
			return
		}
		runtime.UnlockOSThread()
		done <- a
	}()
	a := <-done
	return a.sid, a.err
}

// Names of the two ways threadAccount reads a token, for its errors.
const (
	viaOpened    = "the thread token opened as the service"
	viaEffective = "the thread's effective token"
)

// threadAccount returns the account of the client the calling thread acts
// as: from the thread's token opened as the service, and only when Windows
// denies that open, through the thread's effective-token pseudo-handle.
// Every other failure, and an anonymous client, is an error that names the
// way that failed.
func threadAccount() (string, error) {
	tok, err := openThreadToken()
	if err == nil {
		defer func() { _ = tok.Close() }()
		return tokenAccount(tok, viaOpened)
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return "", err
	}
	// Windows checks the open against the client token's own DACL, which
	// comes from the client's default DACL: the user, SYSTEM and the logon
	// SID, never a virtual account such as NT SERVICE\straza-service. The
	// effective-token pseudo-handle of Windows 8 and later carries
	// TOKEN_QUERY and needs no open.
	sid, perr := effectiveAccount()
	if perr != nil {
		return "", fmt.Errorf("%w, then %w", err, perr)
	}
	return sid, nil
}

// openThreadToken opens the calling thread's token for query as the
// service, because a thread that acts as a client who dialed at
// identification level cannot open anything as that client.
func openThreadToken() (windows.Token, error) {
	var tok windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &tok); err != nil {
		return 0, fmt.Errorf("open the pipe client's token as the service: %w", err)
	}
	return tok, nil
}

// effectiveAccount returns the account of the calling thread's effective
// token, read through its pseudo-handle.
func effectiveAccount() (string, error) {
	return tokenAccount(windows.GetCurrentThreadEffectiveToken(), viaEffective)
}

// tokenAccount returns the user SID of tok, which via names in errors. It
// refuses a token that is not an impersonation token, because the
// effective token of a thread that acts as no one is the service's own,
// and it refuses an anonymous client.
func tokenAccount(tok windows.Token, via string) (string, error) {
	var typ, n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenType, (*byte)(unsafe.Pointer(&typ)), // #nosec G103 -- TokenType fills one uint32
		uint32(unsafe.Sizeof(typ)), &n); err != nil {
		return "", fmt.Errorf("read the type of %s: %w", via, err)
	}
	if typ != windows.TokenImpersonation {
		return "", fmt.Errorf("%s is the service's own token, not the pipe client's", via)
	}
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read the pipe client's account from %s: %w", via, err)
	}
	if u.User.Sid.IsWellKnown(windows.WinAnonymousSid) {
		return "", fmt.Errorf("the pipe client is anonymous, read from %s", via)
	}
	return sidString(u.User.Sid)
}

// sidString spells sid as a Caller's UID. An empty spelling is an error,
// because it would name no account's folder.
func sidString(sid *windows.SID) (string, error) {
	s := sid.String()
	if s == "" {
		return "", errors.New("the account's SID has no string form")
	}
	return s, nil
}

// ownUID is the SID of the account the service runs as, spelled as a
// Caller's UID, or empty when Windows does not name it.
func ownUID() string {
	sid, _ := tokenUser()
	return sid
}

// tokenUser returns the SID of the user of the service's process token.
func tokenUser() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return sidString(u.User.Sid)
}

// isUID reports whether name is a SID as a Caller spells it, so a folder in
// the state directory with any other name is never taken for an account's.
func isUID(name string) bool {
	sid, err := windows.StringToSid(name)
	return err == nil && sid.String() == name
}

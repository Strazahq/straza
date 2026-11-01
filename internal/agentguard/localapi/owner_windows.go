package localapi

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// PipeAccess is the access a thin client opens the service's pipe with,
// and all the pipe grants accounts other than the service and SYSTEM: read
// and write without FILE_APPEND_DATA. On a pipe that bit is
// FILE_CREATE_PIPE_INSTANCE, which would let a client serve on the pipe's
// name.
const PipeAccess = windows.FILE_GENERIC_READ | (windows.FILE_GENERIC_WRITE &^ windows.FILE_APPEND_DATA)

// pipeOwner is the SID the service's pipe must belong to. Tests point it at
// their own account.
var pipeOwner = ServiceSID

// dial opens the service's pipe at identification level, so the program
// that answers can tell who calls but can never act as the caller, and
// waits out a busy pipe until ctx ends. Before the first write it checks
// that the pipe belongs to the service's account, and it closes any other
// pipe unwritten.
func (c *Client) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	conn, err := winio.DialPipeAccessImpLevel(ctx, c.socket, PipeAccess, winio.PipeImpLevelIdentification)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	if err := checkPipeOwner(conn, c.socket); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	return conn, nil
}

// checkPipeOwner refuses the pipe conn opened at path unless pipeOwner owns
// it. An owner it cannot read refuses too.
func checkPipeOwner(conn net.Conn, path string) error {
	f, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("the connection to %s is %T, not a named pipe", path, conn)
	}
	owner, err := ownerOf(windows.Handle(f.Fd()), windows.SE_KERNEL_OBJECT)
	if err != nil {
		return fmt.Errorf("Straza could not read who owns %s (%w), so it sent it nothing. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Ask your administrator to check the straza-service service on this computer", path, err)
	}
	if owner != pipeOwner {
		return fmt.Errorf("The program answering on %s is not the Straza background service, so Straza sent it nothing. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Its pipe belongs to %s, not to NT SERVICE\\%s. Ask your administrator to check the straza-service service "+
			"on this computer", path, owner, ServiceAccount)
	}
	return nil
}

// ownerOf returns the SID of the owner of the object h opens.
func ownerOf(h windows.Handle, kind windows.SE_OBJECT_TYPE) (string, error) {
	sd, err := windows.GetSecurityInfo(h, kind, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return "", err
	}
	if owner == nil {
		return "", errors.New("the object has no owner")
	}
	return owner.String(), nil
}

// SystemPresent reports true on Windows, where the thin clients always use
// the system service and STRAZA_HOME never selects another.
func SystemPresent() bool { return true }

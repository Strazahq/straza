//go:build windows

package service

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sidBuiltinUsers is the SID of Builtin Users, every local account, whose
// git reads the host file.
const sidBuiltinUsers = "S-1-5-32-545"

// fileReadExecute is the read and execute right of a file or folder: read
// its data, attributes, extended attributes and descriptor, list or run
// it, and wait on it.
const fileReadExecute = 0x1200a9

// gitDirSDDL is the security descriptor the service creates the git
// folder with: the state folder's, plus read and execute for Builtin Users
// on the folder and everything inside it. No file in that folder is ever
// secret.
func gitDirSDDL(own string) string {
	return stateDirSDDL(own) + "(A;OICI;0x1200a9;;;BU)"
}

// prepareGitDir creates the host file's folder dir with gitDirSDDL, so the
// temp file and the host file inherit its DACL, and then checks it with
// gitDirTrusted, so one that already existed must pass too. It runs before
// every write, because whoever can change that folder writes git settings
// for every local user's agent. Every failure refuses.
func prepareGitDir(dir string) error {
	own := ownUID()
	sd, err := windows.SecurityDescriptorFromString(gitDirSDDL(own))
	if err != nil {
		return fmt.Errorf("build the access rules of the git folder %s: %w", dir, err)
	}
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return fmt.Errorf("create the git folder %s: %w", dir, err)
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	if err := windows.CreateDirectory(name, &sa); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("create the git folder %s: %w", dir, err)
	}
	sd, attrs, err := pathSecurity(dir)
	if err != nil {
		return fmt.Errorf("`straza service` could not read who owns %s and who may open it (%w), so it will not write git's settings into it. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"An administrator must move that folder away, then start the service again.", dir, err)
	}
	if attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || !gitDirTrusted(sd, own) {
		owner, _, _ := sd.Owner()
		return fmt.Errorf("`straza service` will not write git's settings into %s, because it belongs to %s or lets other accounts in. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"An administrator must move that folder away, then start the service again.", dir, accountName(owner))
	}
	return nil
}

// gitDirTrusted reports whether sd lets only own, SYSTEM and Administrators
// change the git folder: own owns it, and its DACL allows any right only to
// those three, read and execute or less to Builtin Users, and nothing to
// any other account.
func gitDirTrusted(sd *windows.SECURITY_DESCRIPTOR, own string) bool {
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || owner.String() != own {
		return false
	}
	writers := []string{own, sidLocalSystem, sidBuiltinAdmins}
	readers := []string{own, sidLocalSystem, sidBuiltinAdmins, sidBuiltinUsers}
	return daclAllowsOnly(sd, readers, ^uint32(0)) && daclAllowsOnly(sd, writers, ^uint32(fileReadExecute))
}

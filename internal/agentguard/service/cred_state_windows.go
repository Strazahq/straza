//go:build windows

package service

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Well-known SIDs that may own and open the state folder besides the
// service's own account.
const (
	sidLocalSystem   = "S-1-5-18"
	sidBuiltinAdmins = "S-1-5-32-544"
)

// stateDirSDDL is the security descriptor the service creates its state
// folder with: owner own, and a protected DACL that gives own, SYSTEM and
// Administrators full control of the folder and of everything inside it.
func stateDirSDDL(own string) string {
	return "O:" + own + "D:P(A;OICI;FA;;;" + own + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
}

// prepareStateDir creates dir with stateDirSDDL, so every file inside
// inherits that DACL, and then checks the folder with stateDirTrusted, so
// one that already existed must pass too. That folder is the boundary on
// Windows, and the Unix modes the service also sets inside it change
// nothing there. Every failure refuses.
func prepareStateDir(dir string) error {
	own := ownUID()
	if own == "" {
		return errors.New("`straza service` could not read the Windows account it runs as, so it will not open its state folder. " + //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Start it as the Windows service straza-service.")
	}
	sd, err := windows.SecurityDescriptorFromString(stateDirSDDL(own))
	if err != nil {
		return fmt.Errorf("build the access rules of the state directory %s: %w", dir, err)
	}
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return fmt.Errorf("create the state directory %s: %w", dir, err)
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	if err := windows.CreateDirectory(name, &sa); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("create the state directory %s: %w", dir, err)
	}
	sd, attrs, err := pathSecurity(dir)
	if err != nil {
		return fmt.Errorf("`straza service` could not read who owns %s and who may open it (%w), so it will not use that folder. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"An administrator must move that folder away, then start the service again.", dir, err)
	}
	if attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || !stateDirTrusted(sd, own) {
		owner, _, _ := sd.Owner()
		return fmt.Errorf("`straza service` will not use %s, because it belongs to %s or lets other accounts in. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"An administrator must move that folder away, then start the service again.", dir, accountName(owner))
	}
	return nil
}

// pathSecurity opens path itself, never what a link there points to, and
// returns its owner and DACL with its file attributes, read through that
// one handle.
func pathSecurity(path string) (*windows.SECURITY_DESCRIPTOR, uint32, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, 0, err
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
		return nil, 0, err
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, 0, err
	}
	return sd, fi.FileAttributes, nil
}

// stateDirTrusted reports whether sd lets only own, SYSTEM and
// Administrators in: one of them owns it, and its DACL is protected from
// inheritance and allows nothing to any other account.
func stateDirTrusted(sd *windows.SECURITY_DESCRIPTOR, own string) bool {
	trusted := []string{own, sidLocalSystem, sidBuiltinAdmins}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !slices.Contains(trusted, owner.String()) {
		return false
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	return daclAllowsOnly(sd, trusted, ^uint32(0))
}

// daclAllowsOnly reports whether sd has a DACL in which no entry allows a
// right in mask to a SID outside trusted. A missing or NULL DACL allows
// everything, and an entry that is neither an allow nor a deny counts as
// allowing everything, so both fail it.
func daclAllowsOnly(sd *windows.SECURITY_DESCRIPTOR, trusted []string, mask uint32) bool {
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return false
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		default:
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) // #nosec G103 -- the SID of an allow or deny entry starts at SidStart
		if uint32(ace.Mask)&mask != 0 && !slices.Contains(trusted, sid.String()) {
			return false
		}
	}
	return true
}

// accountName names sid for an operator: DOMAIN\name when Windows can look
// it up, else the SID itself.
func accountName(sid *windows.SID) string {
	if sid == nil {
		return "an unknown account"
	}
	name, domain, _, err := sid.LookupAccount("")
	switch {
	case err != nil:
		return sid.String()
	case domain == "":
		return name
	}
	return domain + `\` + name
}

// replaceDurably renames tmp over path with MoveFileEx and then flushes
// path through a handle that may write, because Windows cannot flush a
// directory. It removes tmp when the rename fails.
func replaceDurably(tmp, path string) error {
	from, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		_ = os.Remove(tmp)
		return &os.LinkError{Op: "rename", Old: tmp, New: path, Err: err}
	}
	// Microsoft documents no power-cut guarantee for a rename on one volume:
	// MOVEFILE_WRITE_THROUGH is described for moves that copy, and a
	// directory cannot be flushed. Flushing the renamed file is the most
	// Windows offers. If a power cut still loses a counter, the next
	// check-in repeats a number the server has seen, and the server revokes
	// this machine rather than accept it, which a new enrollment fixes.
	f, err := os.OpenFile(path, os.O_WRONLY, 0) // #nosec G304 -- the service's own state directory
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// readSystemMachine reads MachineGuid under
// HKLM\SOFTWARE\Microsoft\Cryptography in the 64-bit registry view. Windows
// always sets it, so a value that cannot be read stops the service rather
// than fall back to an identity the state folder would carry to a copy.
func readSystemMachine() (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err == nil {
		defer func() { _ = k.Close() }()
		var guid string
		guid, _, err = k.GetStringValue("MachineGuid")
		if guid = strings.TrimSpace(guid); err == nil && guid != "" {
			return guid, nil
		}
		if err == nil {
			err = errors.New("the value is empty")
		}
	}
	return "", fmt.Errorf("`straza service` could not read this computer's MachineGuid under "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
		`HKLM\SOFTWARE\Microsoft\Cryptography (%w), so it cannot bind keys to this computer. `+
		"Check that the value exists and that the straza-service account may read it, then start the service again.", err)
}

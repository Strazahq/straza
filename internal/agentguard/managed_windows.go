//go:build windows

package agentguard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// Well-known SIDs of the accounts that may own and change the managed root.
const (
	sidLocalSystem      = "S-1-5-18"
	sidBuiltinAdmins    = "S-1-5-32-544"
	sidTrustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
)

// managedOwners are the accounts that may own the managed root and what is
// in it, and the only ones that may change them.
var managedOwners = []string{sidLocalSystem, sidBuiltinAdmins, sidTrustedInstaller}

// managedRootDACL is the protected DACL `straza install --managed` gives the
// managed root: SYSTEM and Administrators full control and Users read and
// execute, passed down to every file and folder inside.
const managedRootDACL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)"

// fileDeleteChild is FILE_DELETE_CHILD, which x/sys does not name.
const fileDeleteChild = 0x40

// writeRights are the rights that change a file or folder: its data or its
// entries, its attributes, the deletion of a child or of itself, its DACL
// and its owner, and the generic rights that include writing.
const writeRights = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | fileDeleteChild |
	windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER |
	windows.GENERIC_WRITE | windows.GENERIC_ALL

// tamperProtected reports whether only SYSTEM, Administrators and
// TrustedInstaller can change the file at path and the folder it sits in:
// neither is a link, and each passes adminsOnly. Anything it cannot read
// answers false. A Windows FileInfo carries no owner or DACL, so fi is not
// used.
func tamperProtected(path string, _ os.FileInfo) bool {
	for _, p := range []string{path, filepath.Dir(path)} {
		sd, attrs, err := pathSecurity(p)
		if err != nil || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || !adminsOnly(sd, managedOwners) {
			return false
		}
	}
	return true
}

// adminsOnly reports whether one of trusted owns sd and no entry of its
// DACL allows a write right to any other account. A missing or NULL DACL,
// and an entry that is neither an allow nor a deny, fail it.
func adminsOnly(sd *windows.SECURITY_DESCRIPTOR, trusted []string) bool {
	if !ownedBy(sd, trusted) {
		return false
	}
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
		if uint32(ace.Mask)&writeRights != 0 && !slices.Contains(trusted, sid.String()) {
			return false
		}
	}
	return true
}

// ownedBy reports whether one of trusted owns sd.
func ownedBy(sd *windows.SECURITY_DESCRIPTOR, trusted []string) bool {
	owner, _, err := sd.Owner()
	return err == nil && owner != nil && slices.Contains(trusted, owner.String())
}

// managedConfigFixed reports whether the managed config sits at its fixed
// path in the ProgramData known folder as a regular file that only
// administrators can change. It reads no environment variable, unlike
// ManagedInstalled, so a harness's environment cannot hide a managed
// install from serviceHome.
func managedConfigFixed() bool {
	path := filepath.Join(managedProgramData(), "straza", "config.yaml")
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular() && tamperProtected(path, fi)
}

// managedProgramData is the ProgramData folder that holds the managed root,
// which no environment variable can move (localapi.ProgramData).
func managedProgramData() string { return localapi.ProgramData() }

// runsAsLocalSystem reports whether this process runs as the LocalSystem
// account, as the Straza MSI and Intune run it.
func runsAsLocalSystem() bool {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && u.User.Sid.IsWellKnown(windows.WinLocalSystemSid)
}

// managedRootUntrusted returns the not-protected sentence when a managed
// config is at ManagedConfigPath and ManagedInstalled does not trust it, or
// when it cannot tell.
func managedRootUntrusted() error {
	_, err := os.Lstat(ManagedConfigPath())
	if errors.Is(err, fs.ErrNotExist) || (err == nil && ManagedInstalled()) {
		return nil
	}
	return fmt.Errorf("Straza does not trust %s, because accounts other than administrators can change it. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
		"An administrator must run `straza install --managed` again, which locks the folder.", ManagedRoot())
}

// lockManagedRoot readies the managed root before `straza install
// --managed` writes into it, so that only administrators can change it from
// then on (lockRootAs). With the STRAZA_SYSTEM test seam set it only
// creates the root, as on Linux.
func lockManagedRoot(root string) error {
	if os.Getenv("STRAZA_SYSTEM") != "" {
		if err := os.MkdirAll(root, 0o755); err != nil { // #nosec G301 -- a test's own folder
			return fmt.Errorf("create managed root (need sudo/admin?): %w", err)
		}
		return nil
	}
	admins, err := windows.StringToSid(sidBuiltinAdmins)
	if err != nil {
		return err
	}
	return lockRootAs(root, admins)
}

// lockRootAs creates a missing root with owner owner and managedRootDACL.
// A root that already exists must be a folder and not a link, owned by
// owner or one of managedOwners, with no link inside it (walkManagedRoot),
// or install refuses it. It then gets owner owner and managedRootDACL,
// which Windows passes down to everything inside. Tests pass their own
// account as owner.
func lockRootAs(root string, owner *windows.SID) error {
	want, err := windows.SecurityDescriptorFromString("O:" + owner.String() + managedRootDACL)
	if err != nil {
		return err
	}
	got, attrs, err := pathSecurity(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		name, err := windows.UTF16PtrFromString(root)
		if err != nil {
			return err
		}
		sa := windows.SecurityAttributes{SecurityDescriptor: want}
		sa.Length = uint32(unsafe.Sizeof(sa))
		if err := windows.CreateDirectory(name, &sa); err != nil {
			return fmt.Errorf("create managed root (need sudo/admin?): %w", err)
		}
		return nil
	case err != nil:
		return lockFailed(root, err)
	case attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0:
		return linkRefused(root, root)
	case !ownedBy(got, append(slices.Clone(managedOwners), owner.String())):
		creator, _, _ := got.Owner()
		return fmt.Errorf("`straza install --managed` will not use %s, because %s created it, not an administrator. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Move that folder away, then run the install again.", root, accountName(creator))
	}
	if err := walkManagedRoot(root, owner, false); err != nil {
		return err
	}
	dacl, _, err := want.DACL()
	if err != nil {
		return err
	}
	err = windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil)
	if err != nil {
		return lockFailed(root, err)
	}
	return nil
}

// sealManagedRoot gives every file and folder under root owner
// Administrators and only the DACL entries root passes down, once `straza
// install --managed` wrote them, because the owner Windows gives a new file
// depends on how the install was started. With the STRAZA_SYSTEM test seam
// set it does nothing.
func sealManagedRoot(root string) error {
	if os.Getenv("STRAZA_SYSTEM") != "" {
		return nil
	}
	admins, err := windows.StringToSid(sidBuiltinAdmins)
	if err != nil {
		return err
	}
	return walkManagedRoot(root, admins, true)
}

// walkManagedRoot checks every file and folder inside root: none may be a
// link, and each must be owned by owner or one of managedOwners, or install
// refuses root. With reset, which runs after the install wrote into the
// locked root, the account running the install may own them too, and each
// then gets owner owner, when another account owns it, and only the DACL
// entries it inherits. A folder comes before what is in it, so it is reset
// before it is listed. Tests pass their own account as owner.
func walkManagedRoot(root string, owner *windows.SID, reset bool) error {
	trusted := append(slices.Clone(managedOwners), owner.String())
	// Before the lock, a file the running account owns may be one an agent
	// made as that person, so only the pass after the lock trusts it.
	if u, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil && reset {
		trusted = append(trusted, u.User.Sid.String())
	}
	empty, err := windows.SecurityDescriptorFromString("D:")
	if err != nil {
		return err
	}
	inherited, _, err := empty.DACL()
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return lockFailed(root, err)
		}
		if path == root {
			return nil
		}
		sd, attrs, err := pathSecurity(path)
		switch {
		case err != nil:
			return lockFailed(root, err)
		case attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0:
			return linkRefused(root, path)
		case !ownedBy(sd, trusted):
			creator, _, _ := sd.Owner()
			return fmt.Errorf("`straza install --managed` will not use %s, because %s created %s in it, not an administrator. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
				"Move that folder away, then run the install again.", root, accountName(creator), path)
		case !reset:
			return nil
		}
		info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
		var newOwner *windows.SID
		if current, _, err := sd.Owner(); err != nil || current == nil || !current.Equals(owner) {
			info |= windows.OWNER_SECURITY_INFORMATION
			newOwner = owner
		}
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, newOwner, nil, inherited, nil); err != nil {
			return lockFailed(root, err)
		}
		return nil
	})
}

// linkRefused is the sentence of a managed root that is, or holds, a link.
func linkRefused(root, path string) error {
	if path == root {
		return fmt.Errorf("`straza install --managed` will not use %s, because it is a link or not a folder. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Move it away, then run the install again.", root)
	}
	return fmt.Errorf("`straza install --managed` will not use %s, because %s in it is a link, which the install does not follow. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
		"Move that folder away, then run the install again.", root, path)
}

// lockFailed is the sentence of a managed root install could not read or
// lock.
func lockFailed(root string, err error) error {
	return fmt.Errorf("`straza install --managed` could not lock %s to administrators (%w). "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
		"Run it again from an administrator PowerShell.", root, err)
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

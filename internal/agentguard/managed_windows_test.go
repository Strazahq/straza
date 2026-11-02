//go:build windows

package agentguard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestAdminsOnly pins which owners and DACLs leave a managed root or config
// changeable only by administrators, each in SDDL.
func TestAdminsOnly(t *testing.T) {
	const ti = sidTrustedInstaller
	cases := []struct {
		name string
		sddl string
		want bool
	}{
		{"the root as install locks it", "O:BA" + managedRootDACL, true},
		{"a file in that root", "O:BAD:AI(A;ID;FA;;;SY)(A;ID;FA;;;BA)(A;ID;0x1200a9;;;BU)", true},
		{"owned by SYSTEM", "O:SYD:(A;;FA;;;SY)(A;;FR;;;WD)", true},
		{"owned by TrustedInstaller", "O:" + ti + "D:P(A;;FA;;;" + ti + ")(A;;0x1200a9;;;BU)", true},
		{"a deny for Everyone", "O:BAD:(D;;FW;;;WD)(A;;FA;;;BA)", true},
		{"owned by a user", "O:S-1-5-21-1-2-3-1001D:P(A;OICI;FA;;;SY)", false},
		{"ProgramData's entry for Users", "O:BAD:AI(A;ID;FA;;;SY)(A;ID;FA;;;BA)(A;OICIID;0x1200a9;;;BU)(A;CIID;0x116;;;BU)", false},
		{"CREATOR OWNER on what is made inside", "O:BAD:(A;;FA;;;BA)(A;OICIIO;FA;;;CO)", false},
		{"Everyone may delete it", "O:BAD:(A;;FA;;;BA)(A;;SD;;;WD)", false},
		{"Everyone may change its DACL", "O:BAD:(A;;FA;;;BA)(A;;WD;;;WD)", false},
		{"Everyone may take it over", "O:BAD:(A;;FA;;;BA)(A;;WO;;;WD)", false},
		{"Users may write what is made inside", "O:BAD:(A;;FA;;;BA)(A;OICIIO;GW;;;BU)", false},
		{"Authenticated Users may modify it", "O:BAD:(A;;FA;;;BA)(A;;0x1301bf;;;AU)", false},
		{"Users may delete what is inside", "O:BAD:(A;;FA;;;BA)(A;;0x40;;;BU)", false},
		{"no DACL", "O:BA", false},
		{"a NULL DACL", "O:BAD:NO_ACCESS_CONTROL", false},
		{"an object entry", "O:BAD:(A;;FA;;;BA)(OA;;FR;bf967a86-0de6-11d0-a285-00aa003049e2;;WD)", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatalf("SDDL %q: %v", tc.sddl, err)
			}
			if got := adminsOnly(sd, managedOwners); got != tc.want {
				t.Errorf("adminsOnly(%s) = %v, want %v", tc.sddl, got, tc.want)
			}
		})
	}
}

// TestLockManagedRoot pins what `straza install --managed` does to the
// managed root on Windows, on a temp folder the test's own account owns in
// place of Administrators. lockRootAs creates a missing root, or locks one
// an earlier install left, with that owner and managedRootDACL, and the
// seal leaves every file and folder inside with that owner and only the
// entries the root passes down. adminsOnly accepts all of it. A
// standard account cannot write into the locked root, so the files here
// are the earlier install's.
func TestLockManagedRoot(t *testing.T) {
	me := ownAccount(t)
	trusted := append(slices.Clone(managedOwners), me.String())
	for _, existing := range []bool{false, true} {
		name := "a missing root"
		if existing {
			name = "a root an earlier install left"
		}
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "straza")
			var inside []string
			if existing {
				inside = []string{"config.yaml", "bin", filepath.Join("bin", "straza.exe")}
				writeManaged(t, root, "config.yaml", "serverUrl: https://straza.example\n")
				writeManaged(t, root, filepath.Join("bin", "straza.exe"), "binary")
			}
			t.Cleanup(func() { unlockForCleanup(t, root, me) })
			if err := lockRootAs(root, me); err != nil {
				t.Fatalf("lockRootAs: %v", err)
			}
			if err := walkManagedRoot(root, me, true); err != nil {
				t.Fatalf("walkManagedRoot: %v", err)
			}
			sd := mustSecurity(t, root)
			t.Logf("managed root %s: %s", root, sd)
			control, _, err := sd.Control()
			if err != nil || control&windows.SE_DACL_PROTECTED == 0 || !adminsOnly(sd, trusted) || ownInherited(t, sd) != [2]int{3, 0} {
				t.Errorf("managed root %s, want O:%s%s", sd, me, managedRootDACL)
			}
			for _, rel := range inside {
				path := filepath.Join(root, rel)
				sd := mustSecurity(t, path)
				t.Logf("%s: %s", path, sd)
				if owner, _, err := sd.Owner(); err != nil || !owner.Equals(me) {
					t.Errorf("%s: owner %v (%v), want %s", path, owner, err, me)
				}
				if !adminsOnly(sd, trusted) || ownInherited(t, sd) != [2]int{0, 3} {
					t.Errorf("%s: %s, want only the three entries the root passes down", path, sd)
				}
			}
		})
	}
}

// TestLockManagedRootRefuses pins that install refuses a managed root that
// an ordinary account made, with the sentence that names that account, and
// leaves the folder as it was.
func TestLockManagedRootRefuses(t *testing.T) {
	root := filepath.Join(t.TempDir(), "straza")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	before := mustSecurity(t, root)
	if ownedBy(before, managedOwners) {
		t.Skip("this test runs elevated, so its folders belong to Administrators")
	}
	admins, err := windows.StringToSid(sidBuiltinAdmins)
	if err != nil {
		t.Fatal(err)
	}
	creator, _, _ := before.Owner()
	want := "`straza install --managed` will not use " + root + ", because " + accountName(creator) + " created it, not an administrator."
	if err := lockRootAs(root, admins); err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("lockRootAs on a folder %s made: got %v, want the sentence starting %q", accountName(creator), err, want)
	}
	if after := mustSecurity(t, root); after.String() != before.String() {
		t.Errorf("the refused root changed from %s to %s", before, after)
	}
}

// TestWalkBeforeTheLockRefusesTheRunningAccount pins that the check before
// the lock refuses a file the account running the install owns, which an
// agent running as that person could have made, while the pass after the
// lock accepts it.
func TestWalkBeforeTheLockRefusesTheRunningAccount(t *testing.T) {
	root := filepath.Join(t.TempDir(), "straza")
	file := filepath.Join(root, "config.yaml")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("server: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ownedBy(mustSecurity(t, file), managedOwners) {
		t.Skip("this test runs elevated, so its files belong to Administrators")
	}
	admins, err := windows.StringToSid(sidBuiltinAdmins)
	if err != nil {
		t.Fatal(err)
	}
	want := "`straza install --managed` will not use " + root + ", because "
	if err := walkManagedRoot(root, admins, false); err == nil || !strings.HasPrefix(err.Error(), want) ||
		!strings.Contains(err.Error(), "created "+file+" in it") {
		t.Fatalf("the check before the lock on a file the running account made: got %v, want the refusal naming %s", err, file)
	}
}

// TestLockManagedRootRefusesALink pins that install refuses a managed root
// that holds a link, which it would otherwise follow with administrator
// rights.
func TestLockManagedRootRefusesALink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "straza")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "bin")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("this account cannot make a symbolic link without Developer Mode: %v", err)
	}
	want := "`straza install --managed` will not use " + root + ", because " + link + " in it is a link"
	if err := lockRootAs(root, ownAccount(t)); err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("lockRootAs on a root holding a link: got %v, want the sentence starting %q", err, want)
	}
}

// TestTamperProtectedRefusesAUserFile is the negative control of
// tamperProtected: a config the test's own account wrote is not trusted.
func TestTamperProtectedRefusesAUserFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("serverUrl: https://rogue.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if tamperProtected(path, fi) {
		t.Errorf("tamperProtected trusts %s, which %s", path, mustSecurity(t, path))
	}
}

// TestManagedRootIgnoresTheEnvironment pins that the managed root and its
// bin folder sit in the ProgramData known folder, whatever the ProgramData
// and SystemDrive environment variables say.
func TestManagedRootIgnoresTheEnvironment(t *testing.T) {
	t.Setenv("STRAZA_SYSTEM", "")
	t.Setenv("STRAZA_MANAGED_BIN_DIR", "")
	known, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ProgramData", t.TempDir())
	t.Setenv("SystemDrive", "Z:")
	if got, want := ManagedRoot(), filepath.Join(known, "straza"); got != want {
		t.Errorf("ManagedRoot() = %q, want %q", got, want)
	}
	if got, want := ManagedBinDir(), filepath.Join(known, "straza", "bin"); got != want {
		t.Errorf("ManagedBinDir() = %q, want %q", got, want)
	}
}

// ownAccount returns the SID of the account the test runs as.
func ownAccount(t *testing.T) *windows.SID {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := u.User.Sid.Copy()
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

// writeManaged writes body to rel under root, making the folders on the
// way, as install does.
func writeManaged(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ownInherited counts the entries of sd's DACL: those set on the object
// itself, then those it inherited.
func ownInherited(t *testing.T, sd *windows.SECURITY_DESCRIPTOR) [2]int {
	t.Helper()
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("%s has no DACL: %v", sd, err)
	}
	var n [2]int
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			n[1]++
		} else {
			n[0]++
		}
	}
	return n
}

// unlockForCleanup gives the test's account full control of root and
// everything in it again, which its owner may always do, so the temp
// folder can be removed.
func unlockForCleanup(t *testing.T, root string, me *windows.SID) {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + me.String() + ")")
	if err != nil {
		t.Error(err)
		return
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Error(err)
		return
	}
	err = windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("give %s back to the test for its removal: %v", root, err)
	}
}

// mustSecurity returns the owner and DACL of path.
func mustSecurity(t *testing.T, path string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, _, err := pathSecurity(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return sd
}

//go:build windows

package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// serviceSID is the SID of NT SERVICE\straza-service, the account the
// tables below give the state folder.
const serviceSID = "S-1-5-80-4097175862-1602505720-2885049577-3910644125-2155426594"

// TestStateDirTrusted pins which state folders the service accepts, by
// the owner and DACL of each in SDDL.
func TestStateDirTrusted(t *testing.T) {
	cases := []struct {
		name string
		sddl string
		want bool
	}{
		{"as the service makes it", stateDirSDDL(serviceSID), true},
		{"owned by SYSTEM", "O:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", true},
		{"owned by Administrators", "O:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;" + serviceSID + ")", true},
		{"a deny for Everyone", "O:SYD:P(D;OICI;FA;;;WD)(A;OICI;FA;;;SY)", true},
		{"owned by a user", "O:S-1-5-21-1-2-3-1001D:P(A;OICI;FA;;;SY)", false},
		{"owned by another service", "O:S-1-5-80-1-2-3-4-5D:P(A;OICI;FA;;;SY)", false},
		{"inherits from its parent", "O:SYD:(A;OICI;FA;;;SY)", false},
		{"Everyone may read", "O:SYD:P(A;OICI;FA;;;SY)(A;OICI;FR;;;WD)", false},
		{"Users may read what is made inside", "O:SYD:P(A;OICI;FA;;;SY)(A;OICIIO;FR;;;BU)", false},
		{"Authenticated Users may list it", "O:SYD:P(A;OICI;FA;;;SY)(A;;0x1;;;AU)", false},
		{"no DACL", "O:SY", false},
		{"a NULL DACL", "O:SYD:NO_ACCESS_CONTROL", false},
		{"an object entry", "O:SYD:P(A;OICI;FA;;;SY)(OA;;FA;bf967a86-0de6-11d0-a285-00aa003049e2;;WD)", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatalf("SDDL %q: %v", tc.sddl, err)
			}
			if got := stateDirTrusted(sd, serviceSID); got != tc.want {
				t.Errorf("stateDirTrusted(%s) = %v, want %v", tc.sddl, got, tc.want)
			}
		})
	}
}

// TestCredStateFolderDACL is the Windows twin of TestCredFileModes. The
// service creates its state folder owned by its own account with a
// protected DACL for that account, SYSTEM and Administrators, and the
// lock, the user's folder and every file in it inherit that DACL.
func TestCredStateFolderDACL(t *testing.T) {
	r := newCredRig(t)
	r.enroll(alice)
	if _, err := r.start(alice, "w1"); err != nil {
		t.Fatalf("start: %v", err)
	}
	own := ownUID()
	sd, _, err := pathSecurity(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("state folder %s: %s", r.dir, sd)
	if owner, _, err := sd.Owner(); err != nil || owner.String() != own {
		t.Errorf("state folder owner %v (%v), want the service's account %s", owner, err, own)
	}
	if !stateDirTrusted(sd, own) || len(daclEntries(t, sd)) != 3 {
		t.Errorf("state folder %s, want %s", sd, stateDirSDDL(own))
	}
	trusted := []string{own, sidLocalSystem, sidBuiltinAdmins}
	user := filepath.Join(r.dir, "u", alice.UID)
	for _, path := range []string{
		filepath.Join(r.dir, "lock"), filepath.Join(r.dir, "u"), user, filepath.Join(user, keyFile),
		filepath.Join(user, recordFile), filepath.Join(user, snapshotFile), filepath.Join(user, sessionsFile),
	} {
		sd, _, err := pathSecurity(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		entries := daclEntries(t, sd)
		if len(entries) == 0 || !daclAllowsOnly(sd, trusted, ^uint32(0)) {
			t.Errorf("%s: %s, want only the state folder's inherited entries", path, sd)
		}
		for _, ace := range entries {
			if ace.Header.AceFlags&windows.INHERITED_ACE == 0 {
				t.Errorf("%s: %s carries an entry it did not inherit", path, sd)
			}
		}
	}
}

// TestCredStateFolderRefused pins that the service refuses a state folder
// someone else made, with the state-folder sentence that names the
// folder's owner. A standard user cannot give a folder another owner, so
// the refused folders are the test's own with a DACL that lets other
// accounts in.
func TestCredStateFolderRefused(t *testing.T) {
	machineFrom(t, filepath.Join(t.TempDir(), "machine-id"))
	own := ownUID()
	cases := []struct {
		name string
		sddl string // "" makes the folder with the DACL its parent passes down
		want bool
	}{
		{"made as the service makes it", stateDirSDDL(own), true},
		{"made with its parent's DACL", "", false},
		{"protected but open to Everyone", "D:P(A;OICI;FA;;;" + own + ")(A;OICI;FA;;;WD)", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			if tc.sddl == "" {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				makeFolder(t, dir, tc.sddl)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, err := newCredentials(ctx, Options{StateDir: dir})
			if s != nil {
				defer s.close()
			}
			if tc.want {
				if err != nil {
					t.Fatalf("newCredentials on a folder made as the service makes it: %v", err)
				}
				return
			}
			sd, _, rerr := pathSecurity(dir)
			if rerr != nil {
				t.Fatal(rerr)
			}
			owner, _, _ := sd.Owner()
			want := "`straza service` will not use " + dir + ", because it belongs to " + accountName(owner) + " or lets other accounts in."
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("newCredentials on %s: got %v, want the sentence starting %q", sd, err, want)
			}
		})
	}
}

// TestReadSystemMachine is the positive control of the Windows machine
// identity: MachineGuid reads as a GUID.
func TestReadSystemMachine(t *testing.T) {
	id, err := readSystemMachine()
	if err != nil || len(id) != 36 || strings.Count(id, "-") != 4 {
		t.Fatalf("readSystemMachine() = %q, %v, want a GUID", id, err)
	}
}

// makeFolder creates dir with the security descriptor sddl.
func makeFolder(t *testing.T, dir, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("SDDL %q: %v", sddl, err)
	}
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	if err := windows.CreateDirectory(name, &sa); err != nil {
		t.Fatalf("create %s with %s: %v", dir, sddl, err)
	}
}

// daclEntries returns the entries of sd's DACL.
func daclEntries(t *testing.T, sd *windows.SECURITY_DESCRIPTOR) []*windows.ACCESS_ALLOWED_ACE {
	t.Helper()
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("%s has no DACL: %v", sd, err)
	}
	var out []*windows.ACCESS_ALLOWED_ACE
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		out = append(out, ace)
	}
	return out
}

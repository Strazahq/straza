//go:build windows

package service

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestGitDirTrusted pins which git folders the service writes the host
// file into, by the owner and DACL of each in SDDL.
func TestGitDirTrusted(t *testing.T) {
	own := "O:" + serviceSID + "D:P(A;OICI;FA;;;" + serviceSID + ")"
	cases := []struct {
		name string
		sddl string
		want bool
	}{
		{"as the service makes it", gitDirSDDL(serviceSID), true},
		{"without Users", stateDirSDDL(serviceSID), true},
		{"Users may only read", own + "(A;OICI;FR;;;BU)", true},
		{"a deny for Everyone", gitDirSDDL(serviceSID) + "(D;OICI;FA;;;WD)", true},
		{"Users may change files", own + "(A;OICI;0x1301bf;;;BU)", false},
		{"Users may create files", own + "(A;OICI;0x1200ab;;;BU)", false},
		{"Users may change the DACL", own + "(A;OICI;0x1600a9;;;BU)", false},
		{"Everyone may read", gitDirSDDL(serviceSID) + "(A;OICI;FR;;;WD)", false},
		{"Authenticated Users may list it", gitDirSDDL(serviceSID) + "(A;;0x1;;;AU)", false},
		{"owned by SYSTEM", "O:SYD:P(A;OICI;FA;;;SY)(A;OICI;0x1200a9;;;BU)", false},
		{"owned by Administrators", "O:BAD:P(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)", false},
		{"owned by a user", "O:S-1-5-21-1-2-3-1001D:P(A;OICI;FA;;;SY)", false},
		{"no DACL", "O:" + serviceSID, false},
		{"a NULL DACL", "O:" + serviceSID + "D:NO_ACCESS_CONTROL", false},
		{"an object entry", own + "(OA;;FA;bf967a86-0de6-11d0-a285-00aa003049e2;;WD)", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatalf("SDDL %q: %v", tc.sddl, err)
			}
			if got := gitDirTrusted(sd, serviceSID); got != tc.want {
				t.Errorf("gitDirTrusted(%s) = %v, want %v", tc.sddl, got, tc.want)
			}
		})
	}
}

// TestHostFileFolderDACL is the Windows twin of TestWriteHostFile. The
// system service makes the git folder in its state folder, owned by its
// own account with a protected DACL that adds read and execute for
// Builtin Users, and the host file inherits that DACL, so every local
// user's git can read the file and only the service, SYSTEM and
// Administrators can change it.
func TestHostFileFolderDACL(t *testing.T) {
	own := ownUID()
	s := &credStore{opts: Options{StateDir: t.TempDir()}, log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	s.writeHostFile([]string{"https://github.com"})
	path := hostFilePath(s.opts)
	if raw, err := os.ReadFile(path); err != nil || len(raw) == 0 {
		t.Fatalf("host file %s = %q (%v)", path, raw, err)
	}
	want := map[string]uint32{own: 0x1f01ff, sidLocalSystem: 0x1f01ff, sidBuiltinAdmins: 0x1f01ff, sidBuiltinUsers: fileReadExecute}
	sd, attrs, err := pathSecurity(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("git folder %s: %s", filepath.Dir(path), sd)
	if owner, _, err := sd.Owner(); err != nil || owner.String() != own {
		t.Errorf("git folder owner %v (%v), want the service's account %s", owner, err, own)
	}
	if control, _, err := sd.Control(); err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("git folder control %#x (%v), want a protected DACL", control, err)
	}
	if attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		t.Errorf("git folder attributes %#x, want a plain directory", attrs)
	}
	checkEntries(t, filepath.Dir(path), sd, want, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE)
	sd, _, err = pathSecurity(path)
	if err != nil {
		t.Fatal(err)
	}
	checkEntries(t, path, sd, want, windows.INHERITED_ACE)
}

// TestHostFileFolderRefused pins that the service refuses to write into a
// git folder that lets other accounts change it, or that is a junction,
// with the git folder sentence that names the folder's owner, and that it
// writes no file. A standard user cannot give a folder another owner, so
// the refused folders are the test's own.
func TestHostFileFolderRefused(t *testing.T) {
	own := ownUID()
	cases := []struct {
		name string
		sddl string // "" makes the folder a junction to a folder made as the service makes it
		want bool
	}{
		{"made as the service makes it", gitDirSDDL(own), true},
		{"Users may change files", stateDirSDDL(own) + "(A;OICI;0x1301bf;;;BU)", false},
		{"Everyone may write", gitDirSDDL(own) + "(A;OICI;FW;;;WD)", false},
		{"a junction", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			s := &credStore{opts: Options{StateDir: t.TempDir()}, log: slog.New(slog.NewJSONHandler(logs, nil))}
			path := hostFilePath(s.opts)
			dir, target := filepath.Dir(path), filepath.Dir(path)
			if tc.sddl == "" {
				target = filepath.Join(t.TempDir(), "elsewhere")
				makeFolder(t, target, gitDirSDDL(own))
				if out, err := exec.Command("cmd", "/c", "mklink", "/J", dir, target).CombinedOutput(); err != nil {
					t.Fatalf("mklink /J %s %s: %v %s", dir, target, err, out)
				}
			} else {
				makeFolder(t, dir, tc.sddl)
			}
			s.writeHostFile([]string{"https://github.com"})
			written, _ := filepath.Glob(filepath.Join(target, "*hosts.conf*"))
			if tc.want {
				if len(written) != 1 || logs.Len() != 0 {
					t.Fatalf("files %v and log %s, want the host file alone and no log", written, logs)
				}
				return
			}
			if len(written) != 0 {
				t.Errorf("files %v were written into a refused folder", written)
			}
			sd, _, err := pathSecurity(dir)
			if err != nil {
				t.Fatal(err)
			}
			owner, _, _ := sd.Owner()
			want := "`straza service` will not write git's settings into " + dir + ", because it belongs to " +
				accountName(owner) + " or lets other accounts in. An administrator must move that folder away, then start the service again."
			var line struct {
				Err string `json:"err"`
			}
			if err := json.Unmarshal(logs.Bytes(), &line); err != nil || line.Err != want {
				t.Errorf("log %s (%v), want one line whose err is %q", logs, err, want)
			}
		})
	}
}

// checkEntries checks that sd's DACL gives each SID in want exactly its
// mask and no other SID anything, with flags set on every entry.
func checkEntries(t *testing.T, path string, sd *windows.SECURITY_DESCRIPTOR, want map[string]uint32, flags uint8) {
	t.Helper()
	got := map[string]uint32{}
	for _, ace := range daclEntries(t, sd) {
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&flags != flags {
			t.Errorf("%s: entry type %d flags %#x, want allow entries with flags %#x", path, ace.Header.AceType, ace.Header.AceFlags, flags)
		}
		got[(*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()] = uint32(ace.Mask)
	}
	if len(got) != len(want) {
		t.Errorf("%s: DACL %v, want %v", path, got, want)
	}
	for sid, mask := range want {
		if got[sid] != mask {
			t.Errorf("%s: %s has %#x, want %#x", path, sid, got[sid], mask)
		}
	}
}
